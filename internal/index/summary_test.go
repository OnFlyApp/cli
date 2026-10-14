package index

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

func fakeChat(t *testing.T, calls *atomic.Int32, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if status != 0 {
			http.Error(w, "down", status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "\"Summary: Retries failed network requests.\"\nextra line"}})
	}))
}

// summaryProject writes a one-file project whose parser emits one long chunk.
func summaryProject(t *testing.T, chatURL string) (string, func(t *testing.T) []vectorstore.StoredChunk) {
	t.Helper()
	srv := fakeOllama(t)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	long := "func Fetch() { /* " + "retry the request until the server answers or the budget runs out */ }"
	body := "#!/bin/sh\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"" + long + "\",\"metadata\":{\"name\":\"Fetch\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stored := func(t *testing.T) []vectorstore.StoredChunk {
		st, err := vectorstore.OpenSQLite(filepath.Join(dir, ".onfly", "vector.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		got, err := st.ChunksByFiles(context.Background(), []string{"a.go"})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	return dir, stored
}

func TestSummariesStoredAndReusedAcrossReindex(t *testing.T) {
	var calls atomic.Int32
	chat := fakeChat(t, &calls, 0)
	defer chat.Close()
	dir, stored := summaryProject(t, chat.URL)
	cfg := mustLoad(t, dir)
	cfg.Indexer.Summaries.Provider, cfg.Indexer.Summaries.Endpoint, cfg.Indexer.Summaries.Model = "ollama", chat.URL, "m"
	ctx := context.Background()
	if _, err := Run(ctx, cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	got := stored(t)
	if len(got) != 1 || got[0].Chunk.Metadata["summary"] != "Retries failed network requests." || calls.Load() != 1 {
		t.Fatalf("calls=%d chunks=%+v", calls.Load(), got)
	}
	// The file changes, the chunk does not: the stored summary is reused.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n// touched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	got = stored(t)
	if calls.Load() != 1 || len(got) != 1 || got[0].Chunk.Metadata["summary"] == "" {
		t.Fatalf("reindex must not call the model again: calls=%d chunks=%+v", calls.Load(), got)
	}
}

func TestSummaryModelFailureDoesNotFailIndexing(t *testing.T) {
	var calls atomic.Int32
	chat := fakeChat(t, &calls, http.StatusInternalServerError)
	defer chat.Close()
	dir, stored := summaryProject(t, chat.URL)
	cfg := mustLoad(t, dir)
	cfg.Indexer.Summaries.Provider, cfg.Indexer.Summaries.Endpoint, cfg.Indexer.Summaries.Model = "ollama", chat.URL, "m"
	if _, err := Run(context.Background(), cfg, nil, Options{}); err != nil {
		t.Fatalf("a failing summary model must not fail the run: %v", err)
	}
	got := stored(t)
	if len(got) != 1 || got[0].Chunk.Metadata["summary"] != "" {
		t.Fatalf("%+v", got)
	}
}

func TestSummarizableAndClean(t *testing.T) {
	long := "func F() { /* a body that is long enough to be worth describing in words */ }"
	if !summarizable(parser.Chunk{Content: long, Metadata: map[string]string{"name": "F"}}) {
		t.Error("named long chunk")
	}
	if summarizable(parser.Chunk{Content: "func F() {}", Metadata: map[string]string{"name": "F"}}) {
		t.Error("short chunk")
	}
	if summarizable(parser.Chunk{Content: long, Metadata: map[string]string{}}) {
		t.Error("unnamed non-section chunk")
	}
	if !summarizable(parser.Chunk{Type: "section", Content: long, Metadata: map[string]string{}}) {
		t.Error("doc section")
	}
	if got := cleanSummary("\n  \"Summary: Does a thing.\"\nmore"); got != "Does a thing." {
		t.Errorf("%q", got)
	}
}

func TestEmbedTextIncludesSummary(t *testing.T) {
	c := parser.Chunk{FilePath: "a.go", Content: "code", Metadata: map[string]string{"name": "F", "summary": "Does X."}}
	if got := embedText(c, false); got != "summary: Does X.\n\ncode" {
		t.Fatalf("%q", got)
	}
	if got := embedText(c, true); got != "path: a.go\nsymbol: F\nsummary: Does X.\n\ncode" {
		t.Fatalf("%q", got)
	}
}
