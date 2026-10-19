package mcp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/index"
	"github.com/axitdev/onfly/internal/vectorstore"
)

// The parser turns each "func Name() {}" line into a chunk.
const lineParser = `#!/bin/sh
awk 'BEGIN{printf "["} /^func /{n=$2; sub(/\(.*/,"",n); if (c++) printf ","; printf "{\"type\":\"function\",\"content\":\"%s\",\"metadata\":{\"name\":\"%s\"}}", $0, n} END{print "]"}'
`

// warmProject has an embedder that refuses connections, as when Ollama is not running.
func warmProject(t *testing.T) (*tools, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "parse.sh")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(script, []byte(lineParser), 0o755))
	must(os.MkdirAll(filepath.Join(dir, ".onfly"), 0o755))
	yaml := "project: demo\ndatabase:\n  driver: sqlite\n  dsn: .onfly/vector.db\nembedder:\n  provider: ollama\n  endpoint: http://127.0.0.1:1\n  model: fake\n  dimensions: 4\nindexer:\n  include: [\"**/*.go\"]\n  parsers:\n    - extensions: [\".go\"]\n      type: stdio\n      command: '" + script + "'\nserver:\n  transport: stdio\n  log_file: .onfly/server.log\n"
	must(os.WriteFile(filepath.Join(dir, ".onfly", "config.yaml"), []byte(yaml), 0o644))
	cfg, err := config.Load(dir, "")
	must(err)
	ctx := context.Background()
	store, err := vectorstore.New(ctx, cfg.Database)
	must(err)
	t.Cleanup(func() { store.Close() })
	must(vectorstore.InitAll(ctx, store, cfg))
	h := &tools{cfg: cfg, root: dir, store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), bg: ctx, maxTokens: 8192}
	return h, dir
}

func TestXrayBuildsAndRefreshesTheIndexWithoutAnEmbedder(t *testing.T) {
	h, dir := warmProject(t)
	ctx := context.Background()
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _, err := h.runXray(ctx, nil, xrayIn{Target: "a.go"})
	if err != nil {
		t.Fatalf("an empty index builds on the first call: %v", err)
	}
	if !strings.Contains(resultText(res), "func Old()") {
		t.Fatalf("%s", resultText(res))
	}
	h.bgWG.Wait()
	if defs, _ := h.store.GetComponents(ctx, "Old"); len(defs) != 1 {
		t.Fatalf("Old is not indexed: %v", defs)
	}

	edited := "package p\nfunc Old() {}\nfunc NewlyAddedFunc() {}\n"
	if err := os.WriteFile(file, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _, err = h.runXray(ctx, nil, xrayIn{Target: "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	h.bgWG.Wait()
	text := resultText(res)
	if !strings.Contains(text, "## skeleton") || !strings.Contains(text, "NewlyAddedFunc") {
		t.Fatalf("%s", text)
	}
	if defs, _ := h.store.GetComponents(ctx, "NewlyAddedFunc"); len(defs) != 1 {
		t.Fatalf("the edited file was not re-indexed: %v", defs)
	}
	if stored, ok, _ := h.store.FileHash(ctx, "a.go"); !ok || !index.Fresh(stored, []byte(edited)) {
		t.Fatalf("stored hash %q does not describe the edited file", stored)
	}
}
