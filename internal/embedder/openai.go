package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/axitdev/onfly/internal/config"
)

type openaiEmbedder struct {
	url        string
	model      string
	key        string
	dimensions int
	ask        bool
	client     *http.Client
}

func newOpenAI(cfg config.Embedder) (Embedder, error) {
	endpoint := cfg.Endpoint
	hosted := endpoint == ""
	if hosted {
		endpoint = "https://api.openai.com"
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("embedder.model is required")
	}
	key := cfg.APIKey
	if key == "" && hosted {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key == "" && hosted {
		return nil, fmt.Errorf("embedder: openai api key is required")
	}
	send := hosted
	if cfg.SendDimensions != nil {
		send = *cfg.SendDimensions
	}
	return &openaiEmbedder{
		url:        joinURL(endpoint, "/v1/embeddings"),
		model:      cfg.Model,
		key:        key,
		dimensions: cfg.Dimensions,
		ask:        send,
		client:     newHTTP(),
	}, nil
}

func (e *openaiEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (e *openaiEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	req := struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions int      `json:"dimensions,omitempty"`
	}{Model: e.model, Input: texts}
	if e.ask {
		req.Dimensions = e.dimensions
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := postJSON(ctx, e.client, e.url, e.key, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("embedder: %w", err)
	}
	sort.Slice(resp.Data, func(i, j int) bool { return resp.Data[i].Index < resp.Data[j].Index })
	vecs := make([][]float32, len(resp.Data))
	for i, row := range resp.Data {
		vecs[i] = row.Embedding
	}
	if err := checkDims(vecs, len(texts), e.dimensions); err != nil {
		return nil, err
	}
	return vecs, nil
}
