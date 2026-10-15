package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/axitdev/onfly/internal/config"
)

type Reranker interface {
	// Rerank returns indices into docs, most relevant first, at most topN.
	Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error)
}

// rerankAPI describes one hosted rerank endpoint. Servers that copy the Cohere
// or Jina request shape (vLLM, llama.cpp, Infinity, LiteLLM) work with a custom
// endpoint and need no key.
type rerankAPI struct {
	endpoint, path, keyEnv string
	topField               string
	extra                  map[string]any
}

var rerankAPIs = map[string]rerankAPI{
	"voyage": {"https://api.voyageai.com", "/v1/rerank", "VOYAGE_API_KEY", "top_k", map[string]any{"truncation": true}},
	"cohere": {"https://api.cohere.com", "/v2/rerank", "COHERE_API_KEY", "top_n", nil},
	"jina":   {"https://api.jina.ai", "/v1/rerank", "JINA_API_KEY", "top_n", nil},
}

func NewReranker(cfg config.Reranker) (Reranker, error) {
	if cfg.Provider == "" {
		return nil, nil
	}
	switch cfg.Provider {
	case "ollama":
		return newOllamaReranker(cfg)
	case "ollama-score":
		return newOllamaScoreReranker(cfg)
	}
	api, ok := rerankAPIs[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("reranker provider %q is not available", cfg.Provider)
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("reranker.model is required")
	}
	endpoint := cfg.Endpoint
	hosted := endpoint == ""
	if hosted {
		endpoint = api.endpoint
	}
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv(api.keyEnv)
	}
	if key == "" && hosted {
		return nil, fmt.Errorf("reranker: %s api key is required (reranker.api_key or %s)", cfg.Provider, api.keyEnv)
	}
	return &restReranker{url: joinURL(endpoint, api.path), model: cfg.Model, key: key, api: api, client: newHTTP()}, nil
}

type restReranker struct {
	url, model, key string
	api             rerankAPI
	client          *http.Client
}

func (r *restReranker) Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error) {
	req := map[string]any{"model": r.model, "query": query, "documents": docs, r.api.topField: topN}
	for k, v := range r.api.extra {
		req[k] = v
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := postJSON(ctx, r.client, r.url, r.key, body)
	if err != nil {
		return nil, err
	}
	// Voyage answers under "data"; Cohere and Jina under "results".
	var resp struct {
		Data    []rerankHit `json:"data"`
		Results []rerankHit `json:"results"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("reranker: %w", err)
	}
	hits := resp.Results
	if len(hits) == 0 {
		hits = resp.Data
	}
	out := make([]int, 0, len(hits))
	for _, d := range hits {
		out = append(out, d.Index)
	}
	return out, nil
}

type rerankHit struct {
	Index int `json:"index"`
}

// ollamaReranker asks a chat model to order the candidates in one call. It
// needs no rerank endpoint, so any local model can do it, at LLM latency.
type ollamaReranker struct {
	url, model string
	client     *http.Client
}

const (
	ollamaSnippetRunes = 500
	ollamaNumCtx       = 8192
)

func newOllamaReranker(cfg config.Reranker) (Reranker, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("reranker.model is required (a chat model, for example qwen2.5-coder:7b)")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	return &ollamaReranker{url: joinURL(endpoint, "/api/chat"), model: cfg.Model, client: newHTTP()}, nil
}

func (r *ollamaReranker) Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error) {
	var b strings.Builder
	b.WriteString("You rank code search results for a developer's query.\n")
	b.WriteString("Query: " + query + "\n\nCandidates:\n")
	for i, d := range docs {
		if rs := []rune(d); len(rs) > ollamaSnippetRunes {
			d = string(rs[:ollamaSnippetRunes]) + " ..."
		}
		fmt.Fprintf(&b, "[%d]\n%s\n\n", i+1, d)
	}
	fmt.Fprintf(&b, "List the candidate numbers from most to least relevant to the query. "+
		"Use only numbers from 1 to %d. Answer with JSON only: {\"ranking\": [3, 1, 2]}", len(docs))
	body, err := json.Marshal(map[string]any{
		"model":    r.model,
		"messages": []map[string]string{{"role": "user", "content": b.String()}},
		"stream":   false,
		"format":   "json",
		"options":  map[string]any{"temperature": 0, "num_ctx": ollamaNumCtx},
	})
	if err != nil {
		return nil, err
	}
	raw, err := postJSON(ctx, r.client, r.url, "", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("reranker: %w", err)
	}
	order := parseRanking(resp.Message.Content, len(docs))
	if len(order) == 0 {
		return nil, fmt.Errorf("reranker: model returned no ranking: %.200s", resp.Message.Content)
	}
	if topN > 0 && len(order) > topN {
		order = order[:topN]
	}
	return order, nil
}

var intRe = regexp.MustCompile(`\d+`)

// parseRanking turns a model answer into 0-based indices. It accepts
// {"ranking":[...]}, a bare array, or any text with numbers in it, and drops
// numbers out of range and repeats.
func parseRanking(content string, n int) []int {
	var nums []int
	var obj struct {
		Ranking []int `json:"ranking"`
	}
	switch {
	case json.Unmarshal([]byte(content), &obj) == nil && len(obj.Ranking) > 0:
		nums = obj.Ranking
	case json.Unmarshal([]byte(content), &nums) == nil && len(nums) > 0:
	default:
		nums = nil
		for _, m := range intRe.FindAllString(content, -1) {
			if v, err := strconv.Atoi(m); err == nil {
				nums = append(nums, v)
			}
		}
	}
	seen := make([]bool, n)
	var out []int
	for _, v := range nums {
		if v < 1 || v > n || seen[v-1] {
			continue
		}
		seen[v-1] = true
		out = append(out, v-1)
	}
	return out
}
