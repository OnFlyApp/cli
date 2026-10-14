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
