package mcp

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

func TestGetHistory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\nfunc One() int {\n\treturn 1\n}\n")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-qm", "add One")

	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(ctx, store, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: "func One() int {}", Metadata: map[string]string{"name": "One", "start_line": "3", "end_line": "5"}}, Vector: []float32{1, 0, 0, 0}},
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: "func NoLines() {}", Metadata: map[string]string{"name": "NoLines"}}, Vector: []float32{0, 1, 0, 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &tools{root: dir, store: store}
	_, got, err := h.getHistory(ctx, nil, historyIn{Name: "One"})
	if err != nil || got.Scope != "symbol" || len(got.Commits) != 1 || got.Commits[0].Subject != "add One" {
		t.Fatalf("%+v %v", got, err)
	}
	_, got, err = h.getHistory(ctx, nil, historyIn{Name: "NoLines"})
	if err != nil || got.Scope != "file" || len(got.Commits) != 1 {
		t.Fatalf("file fallback: %+v %v", got, err)
	}
	if _, _, err := h.getHistory(ctx, nil, historyIn{Name: "Missing"}); err == nil {
		t.Fatal("an unknown component is a tool error")
	}
	h.root = t.TempDir() // not a git repository
	if _, _, err := h.getHistory(ctx, nil, historyIn{Name: "One"}); err == nil {
		t.Fatal("outside git the tool errors")
	}
}
