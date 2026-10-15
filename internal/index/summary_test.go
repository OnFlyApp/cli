package index

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestFileChunk(t *testing.T) {
	chunks := []parser.Chunk{
		{Metadata: map[string]string{"name": "A"}}, {Metadata: map[string]string{"name": "B"}},
		{Metadata: map[string]string{"name": "C"}}, {Metadata: map[string]string{}},
	}
	fc, ok := fileChunk("p/a.go", chunks)
	if !ok || fc.Type != "file" || fc.Content != "file: p/a.go\ndefines: A, B, C" {
		t.Fatalf("%+v %v", fc, ok)
	}
	if _, ok := fileChunk("x.go", []parser.Chunk{{Metadata: map[string]string{}}}); ok {
		t.Fatal("a file with no names gets no file chunk")
	}
	if !summarizable(parser.Chunk{Type: "file", Content: fc.Content + " padding padding padding padding"}) {
		t.Fatal("file chunks are summarised")
	}
}

func TestSummariesAndFileChunk(t *testing.T) {
	var calls atomic.Int32
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "A single sentence."}})
	}))
	defer chat.Close()
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	pad := "retry the request until the server answers or the budget runs out"
	body := "#!/bin/sh\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func First() { /* " + pad + " */ }\",\"metadata\":{\"name\":\"First\"}},{\"type\":\"function\",\"content\":\"func Second() { /* " + pad + " twice */ }\",\"metadata\":{\"name\":\"Second\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, dir)
	cfg.Indexer.Summaries.Provider, cfg.Indexer.Summaries.Endpoint, cfg.Indexer.Summaries.Model = "ollama", chat.URL, "m"
	cfg.Indexer.Summaries.Files = true
	if _, err := Run(context.Background(), cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	st, err := vectorstore.OpenSQLite(filepath.Join(dir, ".onfly", "vector.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.ChunksByFiles(context.Background(), []string{"a.go"})
	if err != nil || len(got) != 3 {
		t.Fatalf("two symbols and one file chunk: %+v %v", got, err)
	}
	sums := map[string]string{}
	for _, c := range got {
		sums[c.Chunk.Type+":"+c.Chunk.Metadata["name"]] = c.Chunk.Metadata["summary"]
	}
	if calls.Load() != 3 || sums["function:First"] == "" || sums["function:Second"] == "" || sums["file:"] == "" {
		t.Fatalf("one call per chunk, got %d calls, summaries %v", calls.Load(), sums)
	}
}

func TestSummaryCacheSurvivesDeletingTheDatabase(t *testing.T) {
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
	for _, f := range []string{"vector.db", "vector.db-wal", "vector.db-shm"} {
		os.Remove(filepath.Join(dir, ".onfly", f))
	}
	if _, err := Run(ctx, cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	got := stored(t)
	if calls.Load() != 1 || len(got) != 1 || got[0].Chunk.Metadata["summary"] == "" {
		t.Fatalf("a rebuilt index must take the summary from the cache: calls=%d %+v", calls.Load(), got)
	}
	cfg.Indexer.Summaries.Model = "other"
	os.Remove(filepath.Join(dir, ".onfly", "vector.db"))
	if _, err := Run(ctx, cfg, nil, Options{}); err != nil || calls.Load() != 2 {
		t.Fatalf("a new model must ask again: calls=%d %v", calls.Load(), err)
	}
}

func TestContextForUsesOnlyTheFile(t *testing.T) {
	mk := func(name string, calls ...string) parser.Chunk {
		return parser.Chunk{Metadata: map[string]string{"name": name}, Outbound: calls}
	}
	chunks := []parser.Chunk{
		mk("Run", "Gather", "Fuse", "Printf", "Store.Search"),
		mk("Gather", "Embed"),
		mk("Fuse", "dedupe"),
		mk("dedupe"),
	}
	ctx := contextFor(chunks)
	got := ctx(0)
	if !strings.Contains(got, "Calls (same file): Gather, Fuse") || strings.Contains(got, "Printf") || strings.Contains(got, "Called by") {
		t.Fatalf("%q", got)
	}
	if got := ctx(2); !strings.Contains(got, "Called by (same file): Run") || !strings.Contains(got, "Calls (same file): dedupe") || !strings.Contains(got, "Nearby: ") {
		t.Fatalf("%q", got)
	}
	c := chunks[1]
	c.Content = "func Gather() {}"
	if summaryKey("m", c, "") == summaryKey("m", c, "Calls (same file): x\n") {
		t.Fatal("the context must be part of the cache key")
	}
	if p := summaryPrompt(c, "Calls (same file): x\n"); !strings.Contains(p, "Calls (same file): x") {
		t.Fatalf("%q", p)
	}
}

func TestSummariesOverOpenAIChatWithConcurrency(t *testing.T) {
	var calls, inflight, peak atomic.Int32
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		inflight.Add(-1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "Does one thing."}}}})
	}))
	defer chat.Close()
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	var items []string
	for i := 0; i < 6; i++ {
		items = append(items, fmt.Sprintf(`{"type":"function","content":"func F%d() { /* retry the request until the server answers or the budget runs out %d */ }","metadata":{"name":"F%d"}}`, i, i, i))
	}
	body := "#!/bin/sh\nprintf '%s\\n' '[" + strings.Join(items, ",") + "]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, dir)
	cfg.Indexer.Summaries.Provider, cfg.Indexer.Summaries.Endpoint, cfg.Indexer.Summaries.Model = "openai", chat.URL, "m"
	cfg.Indexer.Summaries.Concurrency = 4
	if _, err := Run(context.Background(), cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	st, err := vectorstore.OpenSQLite(filepath.Join(dir, ".onfly", "vector.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.ChunksByFiles(context.Background(), []string{"a.go"})
	if err != nil || len(got) != 6 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, c := range got {
		if c.Chunk.Metadata["summary"] != "Does one thing." {
			t.Fatalf("every chunk gets its summary: %+v", c.Chunk.Metadata)
		}
	}
	if calls.Load() != 6 || peak.Load() < 2 || peak.Load() > 4 {
		t.Fatalf("calls=%d peak concurrency=%d, want 6 calls overlapping up to 4", calls.Load(), peak.Load())
	}
}

func TestDropTypes(t *testing.T) {
	in := []parser.Chunk{{Type: "use"}, {Type: "class"}, {Type: "Use"}, {Type: "method"}}
	got := dropTypes(in, []string{" USE "})
	if len(got) != 2 || got[0].Type != "class" || got[1].Type != "method" {
		t.Fatalf("%+v", got)
	}
	if len(dropTypes(in, nil)) != 4 {
		t.Fatal("no skip list keeps everything")
	}
}
