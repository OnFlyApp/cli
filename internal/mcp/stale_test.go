package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/index"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

func TestMarkStale(t *testing.T) {
	dir := t.TempDir()
	same := []byte("package a\n")
	for name, body := range map[string][]byte{"same.go": same, "changed.go": []byte("package b\n")} {
		writeFile(t, filepath.Join(dir, name), string(body))
	}
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	var chunks []vectorstore.EmbeddedChunk
	for _, f := range []string{"same.go", "changed.go", "gone.go"} {
		chunks = append(chunks, vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: f, Type: "function", Content: "func F() {}", Metadata: map[string]string{"name": f}}, Vector: []float32{1, 0, 0, 0}})
	}
	if err := storetest.Save(ctx, store, chunks); err != nil {
		t.Fatal(err)
	}
	for f, body := range map[string][]byte{"same.go": same, "changed.go": []byte("package b\n"), "gone.go": []byte("x")} {
		if err := store.CommitFile(ctx, f, index.ContentHash(body), []vectorstore.EmbeddedChunk{{Chunk: parser.Chunk{FilePath: f, Type: "function", Content: "func F() {}", Metadata: map[string]string{"name": f}}, Vector: []float32{1, 0, 0, 0}}}); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "changed.go"), "package b\n// edited\n")
	h := &tools{root: dir, store: store}
	hits := []hitDTO{{FilePath: "same.go"}, {FilePath: "changed.go"}, {FilePath: "gone.go"}, {FilePath: "unknown.go"}, {Lines: "// [Truncated]"}}
	h.markStale(ctx, hits)
	want := []bool{false, true, true, false, false}
	for i, w := range want {
		if hits[i].Stale != w {
			t.Errorf("%s stale=%v want %v", hits[i].FilePath, hits[i].Stale, w)
		}
	}
	if err := os.Remove(filepath.Join(dir, "same.go")); err != nil {
		t.Fatal(err)
	}
}
