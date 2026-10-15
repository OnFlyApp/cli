package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/config"
)

type fakeParser struct {
	chunks []Chunk
	err    error
	closed int
}

func (f *fakeParser) Parse(context.Context, string, []byte) ([]Chunk, error) {
	return f.chunks, f.err
}

func (f *fakeParser) Close() error {
	f.closed++
	return nil
}

func TestRouterLongestExtension(t *testing.T) {
	php := &fakeParser{chunks: []Chunk{{Type: "php"}}}
	blade := &fakeParser{chunks: []Chunk{{Type: "blade"}}}
	r, err := newRouter(config.Indexer{Parsers: []config.ParserConfig{
		{Extensions: []string{".php"}, Type: "wasm", Path: "php.wasm"},
		{Extensions: []string{".blade.php"}, Type: "stdio", Command: "blade"},
	}}, t.TempDir(), func(cfg config.ParserConfig) (Parser, error) {
		if cfg.Type == "stdio" {
			return blade, nil
		}
		return php, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasParser("app/layout.blade.php") {
		t.Fatal("expected blade parser")
	}
	got, err := r.Parse(context.Background(), "app/layout.blade.php", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Type != "blade" {
		t.Fatalf("type = %s", got[0].Type)
	}
	if r.HasParser("readme.txt") {
		t.Fatal("readme.txt should have no parser")
	}
	if _, err := r.Parse(context.Background(), "readme.txt", nil); err == nil || !contains(err.Error(), "no parser") {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterDuplicateExtension(t *testing.T) {
	_, err := newRouter(config.Indexer{Parsers: []config.ParserConfig{
		{Extensions: []string{".php"}, Type: "stdio", Command: "a"},
		{Extensions: []string{".PHP"}, Type: "stdio", Command: "b"},
	}}, t.TempDir(), func(cfg config.ParserConfig) (Parser, error) {
		return &fakeParser{}, nil
	})
	if err == nil || !contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterCloseOnce(t *testing.T) {
	a := &fakeParser{}
	r, err := newRouter(config.Indexer{Parsers: []config.ParserConfig{
		{Extensions: []string{".go", ".mod"}, Type: "stdio", Command: "a"},
	}}, t.TempDir(), func(config.ParserConfig) (Parser, error) { return a, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if a.closed != 1 {
		t.Fatalf("closed %d", a.closed)
	}
}

func TestBladeScriptBeatsPHP(t *testing.T) {
	skipWithoutShell(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "blade.sh")
	body := "#!/bin/sh\nprintf '%s\\n' '[{\"file_path\":\"\",\"type\":\"blade\",\"content\":\"x\",\"metadata\":{\"name\":\"View\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	php := &fakeParser{chunks: []Chunk{{Type: "php"}}}
	r, err := newRouter(config.Indexer{Parsers: []config.ParserConfig{
		{Extensions: []string{".php"}, Type: "wasm", Path: "php.wasm"},
		{Extensions: []string{".blade.php"}, Type: "stdio", Command: script},
	}}, dir, func(cfg config.ParserConfig) (Parser, error) {
		if cfg.Type == "stdio" {
			return newStdioParser(cfg.Command), nil
		}
		return php, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	got, err := r.Parse(context.Background(), "view.blade.php", []byte("<x>"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Type != "blade" || got[0].FilePath != "view.blade.php" {
		t.Fatalf("%+v", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}

func TestRouterScopesParsersByPath(t *testing.T) {
	modern := &fakeParser{chunks: []Chunk{{Type: "modern"}}}
	legacy := &fakeParser{chunks: []Chunk{{Type: "legacy"}}}
	root := t.TempDir()
	r, err := newRouter(config.Indexer{Parsers: []config.ParserConfig{
		{Extensions: []string{".php"}, Type: "stdio", Command: "legacy", Include: []string{"legacy/**"}},
		{Extensions: []string{".php"}, Type: "stdio", Command: "modern", Include: []string{"app/**"}, Exclude: []string{"app/generated/**"}},
	}}, root, func(cfg config.ParserConfig) (Parser, error) {
		if cfg.Command == "legacy" {
			return legacy, nil
		}
		return modern, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"legacy/a.php": "legacy", "app/b.php": "modern", filepath.Join(root, "app", "c.php"): "modern"} {
		got, err := r.Parse(context.Background(), path, nil)
		if err != nil || got[0].Type != want {
			t.Fatalf("%s: %+v %v", path, got, err)
		}
	}
	for _, path := range []string{"other/d.php", "app/generated/e.php"} {
		if r.HasParser(path) {
			t.Fatalf("%s must be out of every parser's scope", path)
		}
	}
}

func TestRouterRejectsOverlapWithoutScopeAndBadPatterns(t *testing.T) {
	open := func(config.ParserConfig) (Parser, error) { return &fakeParser{}, nil }
	_, err := newRouter(config.Indexer{Parsers: []config.ParserConfig{
		{Extensions: []string{".go"}, Type: "stdio", Command: "a", Include: []string{"a/**"}},
		{Extensions: []string{".go"}, Type: "stdio", Command: "b"},
	}}, t.TempDir(), open)
	if err == nil || !contains(err.Error(), "duplicate") {
		t.Fatalf("unscoped overlap: %v", err)
	}
	_, err = newRouter(config.Indexer{Parsers: []config.ParserConfig{
		{Extensions: []string{".go"}, Type: "stdio", Command: "a", Include: []string{"[a"}},
	}}, t.TempDir(), open)
	if err == nil || !contains(err.Error(), "invalid") {
		t.Fatalf("bad pattern: %v", err)
	}
}
