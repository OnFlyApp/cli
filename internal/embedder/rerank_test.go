package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
