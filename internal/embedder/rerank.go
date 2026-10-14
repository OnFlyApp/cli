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
