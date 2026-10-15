package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/axitdev/onfly/internal/config"
)

func TestNewChatDisabledAndInvalid(t *testing.T) {
	if c, err := NewChat(config.LLM{}); c != nil || err != nil {
		t.Fatalf("%v %v", c, err)
	}
	if _, err := NewChat(config.LLM{Provider: "nope", Model: "m"}); err == nil {
		t.Fatal("unknown provider must fail")
	}
	if _, err := NewChat(config.LLM{Provider: "ollama"}); err == nil {
		t.Fatal("a model is required")
	}
}

func TestOllamaChatComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/api/chat" || in.Model != "m" || in.Stream || in.Messages[0].Content != "hi" {
			t.Errorf("path %s body %+v", r.URL.Path, in)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "  answer \n"}})
	}))
	defer srv.Close()
	c, err := NewChat(config.LLM{Provider: "ollama", Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Complete(context.Background(), "hi")
	if err != nil || got != "answer" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestOllamaChatEmptyAnswerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": " "}})
	}))
	defer srv.Close()
	c, _ := NewChat(config.LLM{Provider: "ollama", Endpoint: srv.URL, Model: "m"})
	if _, err := c.Complete(context.Background(), "hi"); err == nil {
		t.Fatal("empty answer must be an error")
	}
}

func TestOpenAIChatToLocalServer(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"message": map[string]string{"content": "<think>hmm</think>\n  The answer.  "}},
		}})
	}))
	defer srv.Close()
	c, err := NewChat(config.LLM{Provider: "openai", Endpoint: srv.URL, Model: "Qwen/Qwen3-8B"})
	if err != nil {
		t.Fatalf("a custom endpoint needs no key: %v", err)
	}
	out, err := c.Complete(context.Background(), "hi")
	if err != nil || out != "The answer." {
		t.Fatalf("%q %v", out, err)
	}
	if auth != "" || got["model"] != "Qwen/Qwen3-8B" || got["temperature"] != float64(0) {
		t.Fatalf("auth %q body %v", auth, got)
	}
	if kw, _ := got["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false {
		t.Fatalf("thinking must be switched off for local servers: %v", got)
	}
	c2, _ := NewChat(config.LLM{Provider: "openai", Endpoint: srv.URL, Model: "m", APIKey: "k"})
	if _, err := c2.Complete(context.Background(), "hi"); err != nil || auth != "Bearer k" {
		t.Fatalf("%q %v", auth, err)
	}
}

func TestOpenAIChatHostedNeedsKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := NewChat(config.LLM{Provider: "openai", Model: "gpt"}); err == nil {
		t.Fatal("the hosted endpoint needs a key")
	}
	if _, err := NewChat(config.LLM{Provider: "openai", Endpoint: "http://x"}); err == nil {
		t.Fatal("a model is required")
	}
}
