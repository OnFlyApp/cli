package parser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGoParserWASM(t *testing.T) {
	wasm := buildGoParser(t)
	p, err := newWasmParser(context.Background(), wasm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	src := []byte("package demo\n\ntype Service struct{}\n\nfunc (s *Service) Execute() {}\n")
	chunks, err := p.Parse("src/svc.go", src)
	if err != nil {
		t.Fatal(err)
	}
	var method Chunk
	found := false
	for _, chunk := range chunks {
		if chunk.Type == "method" && chunk.Metadata["name"] == "Execute" {
			method = chunk
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", chunks)
	}
	if method.Metadata["receiver"] != "Service" || method.FilePath != "src/svc.go" {
		t.Fatalf("%+v", method)
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
