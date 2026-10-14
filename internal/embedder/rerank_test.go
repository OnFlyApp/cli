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
