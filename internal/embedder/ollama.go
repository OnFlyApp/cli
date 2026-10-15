package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/axitdev/onfly/internal/config"
)

type ollamaEmbedder struct {
	url        string
	model      string
	dimensions int
	// numCtx is the context window sent with each request. Ollama loads
	// embedding models with 4096 tokens unless told otherwise and silently cuts
	// longer inputs, so it must cover max_input_tokens plus the prompt prefix.
	numCtx int
	client *http.Client
}

// ctxMargin is added to max_input_tokens for prompt prefixes and summaries.
const ctxMargin = 512

func newOllama(cfg config.Embedder) (Embedder, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("embedder.model is required")
	}
	return &ollamaEmbedder{
		url:        joinURL(endpoint, "/api/embed"),
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		numCtx:     numCtxFor(cfg.MaxInputTokens),
		client:     newHTTP(),
	}, nil
}

func (e *ollamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (e *ollamaEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	req := struct {
		Model   string         `json:"model"`
		Input   []string       `json:"input"`
		Options map[string]any `json:"options,omitempty"`
	}{Model: e.model, Input: texts}
	if e.numCtx > 0 {
		req.Options = map[string]any{"num_ctx": e.numCtx}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := postJSON(ctx, e.client, e.url, "", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("embedder: %w", err)
	}
	if err := checkDims(resp.Embeddings, len(texts), e.dimensions); err != nil {
		return nil, err
	}
	return resp.Embeddings, nil
}

// numCtxFor returns the context window to request for chunks of up to
// maxInput tokens, or 0 to keep the server default when that already fits.
func numCtxFor(maxInput int) int {
	if maxInput <= 0 {
		return 0
	}
	n := maxInput + ctxMargin
	if n <= 4096 {
		return 0
	}
	return n
}
