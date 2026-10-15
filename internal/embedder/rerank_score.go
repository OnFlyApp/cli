package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/axitdev/onfly/internal/config"
)

// scoreReranker scores each candidate on its own with a generative reranking
// model (Qwen3-Reranker style): it asks "does the document answer the query?"
// and reads the probability of "yes" against "no" from the next token's
// log-probabilities. Unlike the listwise reranker it needs one short call per
// candidate, but each call is a single token and calls run in parallel.
type scoreReranker struct {
	// next returns the most likely next tokens for prompt with their
	// log-probabilities.
	next    func(ctx context.Context, prompt string) ([]tokenLogprob, error)
	workers int
}

type tokenLogprob struct {
	Token   string
	Logprob float64
}

const (
	scoreDocRunes = 1500
	scoreWorkers  = 4
	scoreSystem   = "Judge whether the Document meets the requirements based on the Query and the Instruct provided. Note that the answer can only be \"yes\" or \"no\"."
	scoreInstruct = "Given a developer question about a codebase, judge whether the code or text chunk answers it"
)

func newOllamaScoreReranker(cfg config.Reranker) (Reranker, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("reranker.model is required (a Qwen3-Reranker model, for example dengcao/Qwen3-Reranker-4B:Q4_K_M)")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	url, model, hc := joinURL(endpoint, "/api/generate"), cfg.Model, newHTTP()
	return &scoreReranker{workers: scoreWorkers, next: func(ctx context.Context, prompt string) ([]tokenLogprob, error) {
		body, err := json.Marshal(map[string]any{
			"model": model, "prompt": prompt, "raw": true, "stream": false,
			"logprobs": true, "top_logprobs": 20,
			"options": map[string]any{"num_predict": 1, "temperature": 0},
		})
		if err != nil {
			return nil, err
		}
		raw, err := postJSON(ctx, hc, url, "", body)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Logprobs []struct {
				TopLogprobs []struct {
					Token   string  `json:"token"`
					Logprob float64 `json:"logprob"`
				} `json:"top_logprobs"`
			} `json:"logprobs"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("reranker: %w", err)
		}
		if len(resp.Logprobs) == 0 {
			return nil, fmt.Errorf("reranker: the server returned no logprobs (needs an Ollama version with logprobs support)")
		}
		var out []tokenLogprob
		for _, t := range resp.Logprobs[0].TopLogprobs {
			out = append(out, tokenLogprob{t.Token, t.Logprob})
		}
		return out, nil
	}}, nil
}

// newOpenAIScoreReranker is the same scoring over the OpenAI completions API
// (vLLM, mlx_lm.server, llama.cpp, LM Studio), which returns log-probabilities
// as a token-to-logprob map per position.
func newOpenAIScoreReranker(cfg config.Reranker) (Reranker, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("reranker.model is required (a Qwen3-Reranker model)")
	}
	endpoint := cfg.Endpoint
	hosted := endpoint == ""
	if hosted {
		endpoint = "https://api.openai.com"
	}
	key := cfg.APIKey
	if key == "" && hosted {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key == "" && hosted {
		return nil, fmt.Errorf("reranker: an api key is required for the hosted endpoint (reranker.api_key or OPENAI_API_KEY)")
	}
	url, model, hc := joinURL(endpoint, "/v1/completions"), cfg.Model, newHTTP()
	return &scoreReranker{workers: scoreWorkers, next: func(ctx context.Context, prompt string) ([]tokenLogprob, error) {
		body, err := json.Marshal(map[string]any{
			"model": model, "prompt": prompt, "max_tokens": 1, "temperature": 0, "logprobs": 20,
		})
		if err != nil {
			return nil, err
		}
		raw, err := postJSON(ctx, hc, url, key, body)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Choices []struct {
				Logprobs struct {
					TopLogprobs []map[string]float64 `json:"top_logprobs"`
				} `json:"logprobs"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("reranker: %w", err)
		}
		if len(resp.Choices) == 0 || len(resp.Choices[0].Logprobs.TopLogprobs) == 0 {
			return nil, fmt.Errorf("reranker: the server returned no logprobs (completions with logprobs are required)")
		}
		var out []tokenLogprob
		for tok, lp := range resp.Choices[0].Logprobs.TopLogprobs[0] {
			out = append(out, tokenLogprob{tok, lp})
		}
		return out, nil
	}}, nil
}

func scorePrompt(query, doc string) string {
	if r := []rune(doc); len(r) > scoreDocRunes {
		doc = string(r[:scoreDocRunes]) + " ..."
	}
	return "<|im_start|>system\n" + scoreSystem + "<|im_end|>\n" +
		"<|im_start|>user\n<Instruct>: " + scoreInstruct + "\n<Query>: " + query + "\n<Document>: " + doc + "<|im_end|>\n" +
		"<|im_start|>assistant\n<think>\n\n</think>\n\n"
}

func (r *scoreReranker) Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error) {
	scores := make([]float64, len(docs))
	errs := make([]error, len(docs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(r.workers, 1))
	for i, d := range docs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, d string) {
			defer wg.Done()
			defer func() { <-sem }()
			scores[i], errs[i] = r.score(ctx, query, d)
		}(i, d)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	order := make([]int, len(docs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	if topN > 0 && len(order) > topN {
		order = order[:topN]
	}
	return order, nil
}

func (r *scoreReranker) score(ctx context.Context, query, doc string) (float64, error) {
	top, err := r.next(ctx, scorePrompt(query, doc))
	if err != nil {
		return 0, err
	}
	yes, no := math.Inf(-1), math.Inf(-1)
	for _, t := range top {
		switch strings.ToLower(strings.TrimSpace(t.Token)) {
		case "yes":
			yes = math.Max(yes, t.Logprob)
		case "no":
			no = math.Max(no, t.Logprob)
		}
	}
	switch {
	case math.IsInf(yes, -1) && math.IsInf(no, -1):
		return 0, nil // neither answer was likely: rank last
	case math.IsInf(no, -1):
		return 1, nil
	case math.IsInf(yes, -1):
		return 0, nil
	}
	return 1 / (1 + math.Exp(no-yes)), nil
}
