package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/index"
	"github.com/axitdev/onfly/internal/mcp"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestE2EGoParser(t *testing.T) {
	wasm := buildGoParser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vecs := make([][]float32, len(req.Input))
		for i, text := range req.Input {
			vecs[i] = []float32{0, 1, 0, float32(len(text)%40) + 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := runInit(dir, false, false); err != nil {
		t.Fatal(err)
	}
	yaml := "project: demo\ndatabase:\n  driver: sqlite\n  dsn: .onfly/vector.db\nembedder:\n  provider: ollama\n  endpoint: " + srv.URL + "\n  model: fake\n  dimensions: 4\nrules:\n  - .onfly/rules/architecture.md\nindexer:\n  include: [\"src/**/*\"]\n  exclude: [\"vendor\"]\n  parsers:\n    - extensions: [\".go\"]\n      type: wasm\n      path: " + wasm + "\nserver:\n  transport: stdio\n  log_level: info\n  log_file: .onfly/server.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".onfly", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package demo\n\ntype Service struct{}\n\nfunc (s *Service) Execute() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "src", "svc.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stats, err := index.Run(ctx, cfg, nil, index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 || stats.Chunks < 2 {
		t.Fatalf("%+v", stats)
	}
	stats, err = index.Run(ctx, cfg, nil, index.Options{})
	if err != nil || stats.Skipped != 1 || stats.Chunks != 0 || stats.Pruned != 0 {
		t.Fatalf("reindex %+v %v", stats, err)
	}

	store, err := vectorstore.New(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(ctx, cfg.Embedder.Dimensions); err != nil {
		t.Fatal(err)
	}
	chunk, err := store.GetComponentAST(ctx, "Execute")
	if err != nil || chunk.Type != "method" || chunk.Metadata["receiver"] != "Service" {
		t.Fatalf("%v %+v", err, chunk)
	}
	hits, err := store.SearchKeyword(ctx, "execute", 20, "")
	if err != nil || len(hits) != 1 {
		t.Fatalf("%v %+v", err, hits)
	}
	emb, err := embedder.New(cfg.Embedder)
	if err != nil {
		t.Fatal(err)
	}
	srvMCP, closeTrace, err := mcp.NewServer(cfg, store, emb, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTrace()
	st, ct := sdkmcp.NewInMemoryTransports()
	if _, err := srvMCP.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "search_keyword",
		Arguments: map[string]any{"keyword": "Execute"},
	})
	session.Close()
	store.Close()
	if err != nil || res.IsError || !strings.Contains(jsonText(res.StructuredContent), "Execute") {
		t.Fatalf("%v %+v", err, res)
	}

	if err := os.Remove(filepath.Join(dir, "src", "svc.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Run(ctx, cfg, nil, index.Options{}); err != nil {
		t.Fatal(err)
	}
	store, err = vectorstore.New(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(ctx, cfg.Embedder.Dimensions); err != nil {
		t.Fatal(err)
	}
	_, err = store.GetComponentAST(ctx, "Execute")
	if !errors.Is(err, vectorstore.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func buildGoParser(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	clone := exec.Command("gh", "repo", "clone", "OnFlyApp/ext-parser-go", dir, "--", "--depth=1")
	if buf, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone go parser: %v\n%s", err, buf)
	}
	out := filepath.Join(t.TempDir(), "go-parser.wasm")
	build := exec.Command("go", "build", "-o", out, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if buf, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build go parser: %v\n%s", err, buf)
	}
	return out
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
