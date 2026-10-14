package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/axitdev/onfly/internal/config"
)

// Chat asks a language model one question and returns its answer text.
type Chat interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// NewChat builds a Chat for cfg, or returns nil when no provider is set.
// Only ollama is supported: it is local, needs no key, and is what the rerank
// and embed paths already target.
func NewChat(cfg config.LLM) (Chat, error) {
	if cfg.Provider == "" {
		return nil, nil
	}
	if cfg.Provider != "ollama" {
		return nil, fmt.Errorf("provider %q is not available (use ollama)", cfg.Provider)
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("model is required (a chat model, for example qwen2.5-coder:7b)")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	return &ollamaChat{url: joinURL(endpoint, "/api/chat"), model: cfg.Model, client: newHTTP()}, nil
}

type ollamaChat struct {
	url, model string
	client     *http.Client
}

func (c *ollamaChat) Complete(ctx context.Context, prompt string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":    c.model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   false,
		"options":  map[string]any{"temperature": 0, "num_ctx": ollamaNumCtx},
	})
	if err != nil {
		return "", err
	}
	raw, err := postJSON(ctx, c.client, c.url, "", body)
	if err != nil {
		return "", err
	}
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("chat: %w", err)
	}
	out := strings.TrimSpace(resp.Message.Content)
	if out == "" {
		return "", fmt.Errorf("chat: model returned no text")
	}
	return out, nil
}
