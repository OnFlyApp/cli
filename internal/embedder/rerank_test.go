package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
)

func TestVoyageRerankOrdersByScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"index": 2, "relevance_score": 0.9},
			{"index": 0, "relevance_score": 0.4},
		}})
	}))
	defer srv.Close()
	rr, err := NewReranker(config.Reranker{Provider: "voyage", Endpoint: srv.URL, Model: "rerank-2", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := rr.Rerank(context.Background(), "q", []string{"a", "b", "c"}, 2)
	if err != nil || len(got) != 2 || got[0] != 2 || got[1] != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestNewRerankerDisabled(t *testing.T) {
	rr, err := NewReranker(config.Reranker{})
	if rr != nil || err != nil {
		t.Fatalf("%v %v", rr, err)
	}
}

func TestCohereAndJinaRerank(t *testing.T) {
	for provider, wantPath := range map[string]string{"cohere": "/v2/rerank", "jina": "/v1/rerank"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if r.URL.Path != wantPath || in["top_n"] != float64(2) || in["top_k"] != nil {
				t.Errorf("%s: path %s body %v", provider, r.URL.Path, in)
			}
			if r.Header.Get("Authorization") != "" {
				t.Errorf("%s: a custom endpoint without a key must send no Authorization header", provider)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 1}, {"index": 0}}})
		}))
		rr, err := NewReranker(config.Reranker{Provider: provider, Endpoint: srv.URL, Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := rr.Rerank(context.Background(), "q", []string{"a", "b"}, 2)
		srv.Close()
		if err != nil || len(got) != 2 || got[0] != 1 {
			t.Fatalf("%s: %v %v", provider, got, err)
		}
	}
}

func TestHostedRerankerNeedsKey(t *testing.T) {
	t.Setenv("COHERE_API_KEY", "")
	if _, err := NewReranker(config.Reranker{Provider: "cohere", Model: "m"}); err == nil {
		t.Fatal("hosted endpoint without key must fail")
	}
	if _, err := NewReranker(config.Reranker{Provider: "nope", Model: "m"}); err == nil {
		t.Fatal("unknown provider must fail")
	}
}

func TestOllamaLLMRerank(t *testing.T) {
	var prompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model    string `json:"model"`
			Format   string `json:"format"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/api/chat" || in.Model != "qwen" || in.Format != "json" || in.Stream {
			t.Errorf("path %s body %+v", r.URL.Path, in)
		}
		prompt = in.Messages[0].Content
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"ranking": [3, 1, 9, 3, 2]}`}})
	}))
	defer srv.Close()
	rr, err := NewReranker(config.Reranker{Provider: "ollama", Endpoint: srv.URL, Model: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 2000)
	got, err := rr.Rerank(context.Background(), "find retry", []string{"a", "b", long}, 2)
	if err != nil || len(got) != 2 || got[0] != 2 || got[1] != 0 {
		t.Fatalf("out-of-range and repeated numbers must be dropped, then cut to topN: %v %v", got, err)
	}
	if !strings.Contains(prompt, "find retry") || !strings.Contains(prompt, "[3]") || strings.Contains(prompt, strings.Repeat("x", 600)) {
		t.Fatalf("prompt must carry the query, numbered candidates and truncated snippets: %.300s", prompt)
	}
}

func TestParseRankingFormats(t *testing.T) {
	cases := map[string][]int{
		`{"ranking":[2,1]}`:     {1, 0},
		`[2,1]`:                 {1, 0},
		"Best first: 2 then 1.": {1, 0},
		`{"ranking":[0,5,1]}`:   {0},
		`no numbers at all`:     nil,
		`{"ranking":[1,1,1]}`:   {0},
	}
	for in, want := range cases {
		got := parseRanking(in, 3)
		if len(got) != len(want) {
			t.Errorf("%q: %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: %v, want %v", in, got, want)
			}
		}
	}
}

func TestOllamaRerankerErrors(t *testing.T) {
	if _, err := NewReranker(config.Reranker{Provider: "ollama"}); err == nil {
		t.Fatal("a model is required")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "I cannot do that"}})
	}))
	defer srv.Close()
	rr, _ := NewReranker(config.Reranker{Provider: "ollama", Endpoint: srv.URL, Model: "m"})
	if _, err := rr.Rerank(context.Background(), "q", []string{"a", "b"}, 2); err == nil {
		t.Fatal("an answer with no ranking must be an error so the fused order is kept")
	}
}

func TestOllamaScoreRerankerOrdersByYesProbability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Prompt   string `json:"prompt"`
			Raw      bool   `json:"raw"`
			Logprobs bool   `json:"logprobs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/api/generate" || !in.Raw || !in.Logprobs || !strings.Contains(in.Prompt, "<Query>: q") {
			t.Errorf("path %s body %+v", r.URL.Path, in)
		}
		yes, no := -0.1, -2.5
		switch {
		case strings.Contains(in.Prompt, "<Document>: bad"):
			yes, no = -3.0, -0.05
		case strings.Contains(in.Prompt, "<Document>: mid"):
			yes, no = -0.7, -0.7
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"logprobs": []map[string]any{{"top_logprobs": []map[string]any{
			{"token": "yes", "logprob": yes}, {"token": "no", "logprob": no}, {"token": ",", "logprob": -9},
		}}}})
	}))
	defer srv.Close()
	rr, err := NewReranker(config.Reranker{Provider: "ollama-score", Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := rr.Rerank(context.Background(), "q", []string{"bad", "good", "mid"}, 2)
	if err != nil || len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := NewReranker(config.Reranker{Provider: "ollama-score"}); err == nil {
		t.Fatal("a model is required")
	}
}

func TestOllamaScoreRerankerNeedsLogprobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "yes"})
	}))
	defer srv.Close()
	rr, _ := NewReranker(config.Reranker{Provider: "ollama-score", Endpoint: srv.URL, Model: "m"})
	if _, err := rr.Rerank(context.Background(), "q", []string{"a"}, 1); err == nil {
		t.Fatal("a server without logprobs must be an error so the fused order is kept")
	}
}

func TestOpenAIScoreRerankerReadsCompletionLogprobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Prompt    string `json:"prompt"`
			MaxTokens int    `json:"max_tokens"`
			Logprobs  int    `json:"logprobs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/v1/completions" || in.MaxTokens != 1 || in.Logprobs == 0 {
			t.Errorf("path %s body %+v", r.URL.Path, in)
		}
		top := map[string]float64{"yes": -0.1, "no": -2.5}
		if strings.Contains(in.Prompt, "<Document>: bad") {
			top = map[string]float64{"yes": -3, "no": -0.05, "maybe": -5}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"text": "yes", "logprobs": map[string]any{"top_logprobs": []map[string]float64{top}}},
		}})
	}))
	defer srv.Close()
	rr, err := NewReranker(config.Reranker{Provider: "openai-score", Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := rr.Rerank(context.Background(), "q", []string{"bad", "good"}, 2)
	if err != nil || len(got) != 2 || got[0] != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := NewReranker(config.Reranker{Provider: "openai-score", Model: "m"}); err == nil {
		t.Fatal("the hosted endpoint needs a key")
	}
}
