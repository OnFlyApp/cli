package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/axitdev/onfly/internal/config"
)

type Reranker interface {
	// Rerank returns indices into docs, most relevant first, at most topN.
	Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error)
}

func NewReranker(cfg config.Reranker) (Reranker, error) {
	switch cfg.Provider {
	case "":
		return nil, nil
	case "voyage":
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = "https://api.voyageai.com"
		}
		if cfg.Model == "" {
			return nil, fmt.Errorf("reranker.model is required")
		}
		key := cfg.APIKey
		if key == "" {
			key = os.Getenv("VOYAGE_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("reranker: voyage api key is required")
		}
		return &voyageReranker{url: joinURL(endpoint, "/v1/rerank"), model: cfg.Model, key: key, client: newHTTP()}, nil
	default:
		return nil, fmt.Errorf("reranker provider %q is not available", cfg.Provider)
	}
}

type voyageReranker struct {
	url, model, key string
	client          *http.Client
}

func (r *voyageReranker) Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error) {
	body, err := json.Marshal(map[string]any{
		"model": r.model, "query": query, "documents": docs, "top_k": topN, "truncation": true,
	})
	if err != nil {
		return nil, err
	}
	raw, err := postJSON(ctx, r.client, r.url, r.key, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			Index int `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("reranker: %w", err)
	}
	out := make([]int, 0, len(resp.Data))
	for _, d := range resp.Data {
		out = append(out, d.Index)
	}
	return out, nil
}
