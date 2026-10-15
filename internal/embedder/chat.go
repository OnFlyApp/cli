package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/axitdev/onfly/internal/config"
)

type Chat interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

func NewChat(cfg config.LLM) (Chat, error) {
	switch cfg.Provider {
	case "":
		return nil, nil
	case "openai":
		return newOpenAIChat(cfg)
	case "ollama":
	default:
		return nil, fmt.Errorf("provider %q is not available (use ollama or openai)", cfg.Provider)
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
		"think":    false,
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

type openaiChat struct {
	url, model, key string
	custom          bool
	client          *http.Client
}

func newOpenAIChat(cfg config.LLM) (Chat, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	endpoint := cfg.Endpoint
	hosted := endpoint == ""
	if hosted {
		endpoint = "https://api.openai.com"
	}
	key := cfg.APIKey
	if key == "" && hosted {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key == "" && hosted {
		return nil, fmt.Errorf("an api key is required for the hosted endpoint (api_key or OPENAI_API_KEY)")
	}
	return &openaiChat{url: joinURL(endpoint, "/v1/chat/completions"), model: cfg.Model, key: key, custom: !hosted, client: newHTTP()}, nil
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

func (c *openaiChat) Complete(ctx context.Context, prompt string) (string, error) {
	req := map[string]any{
		"model":       c.model,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		"temperature": 0,
		"stream":      false,
	}
	if c.custom {
		req["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	raw, err := postJSON(ctx, c.client, c.url, c.key, body)
	if err != nil {
		return "", err
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("chat: the server returned no choices")
	}
	out := strings.TrimSpace(thinkRe.ReplaceAllString(resp.Choices[0].Message.Content, ""))
	if out == "" {
		return "", fmt.Errorf("chat: model returned no text")
	}
	return out, nil
}
