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

type voyageEmbedder struct {
	url        string
	model      string
	key        string
	dimensions int
	client     *http.Client
}

type voyageBody struct {
	Model           string   `json:"model"`
	Input           []string `json:"input"`
	InputType       string   `json:"input_type"`
	OutputDimension int      `json:"output_dimension,omitempty"`
}

func newVoyage(cfg config.Embedder) (Embedder, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://api.voyageai.com"
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("embedder.model is required")
	}
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv("VOYAGE_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("embedder: voyage api key is required")
	}
	return &voyageEmbedder{
		url:        joinURL(endpoint, "/v1/embeddings"),
		model:      cfg.Model,
		key:        key,
		dimensions: cfg.Dimensions,
		client:     newHTTPFor(cfg.TimeoutSeconds),
	}, nil
}

func (e *voyageEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.embed(ctx, []string{text}, "query")
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (e *voyageEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	return e.embed(ctx, texts, "document")
}

func (e *voyageEmbedder) embed(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	body, err := json.Marshal(voyageBody{
		Model: e.model, Input: texts, InputType: inputType, OutputDimension: e.dimensions,
	})
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
