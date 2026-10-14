package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
)

func TestInitWritesSpecConfig(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, false, false); err != nil {
		t.Fatal(err)
	}
	if err := runInit(dir, false, false); err == nil {
		t.Fatal("expected existing config to fail")
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedder.Model != "qwen3-embedding:0.6b" || cfg.Embedder.Dimensions != 1024 || cfg.Embedder.Provider != "ollama" {
		t.Fatalf("%+v", cfg.Embedder)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("%+v", cfg)
	}
	if len(cfg.Indexer.Include) != 1 || cfg.Indexer.Include[0] != "**/*" {
		t.Fatalf("include %v", cfg.Indexer.Include)
	}
	if cfg.Project != filepath.Base(dir) {
		t.Fatalf("project %q, want the directory name", cfg.Project)
	}
	if _, err := os.Stat(filepath.Join(dir, ".onfly", "plugins")); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".onfly", "rules", "project.md"))
	if err != nil || !strings.Contains(string(body), "Project.Name") {
		t.Fatalf("rules: %v %q", err, body)
	}
	if err := runInit(dir, true, false); err != nil {
		t.Fatal(err)
	}
}

func TestInitConfiguresOnlyTheBuiltInTextParser(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, false, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Indexer.Parsers) != 1 || cfg.Indexer.Parsers[0].Type != "text" {
		t.Fatalf("parsers = %+v", cfg.Indexer.Parsers)
	}
}

func TestInitCIWritesWorkflow(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, false, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".github", "workflows", "onfly-index.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, needle := range []string{"onfly index --ci", "go install github.com/axitdev/onfly@latest"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("missing %s\n%s", needle, text)
		}
	}
	if strings.Contains(text, "yourdomain.com") || strings.Contains(text, "OPENAI_API_KEY") || strings.Contains(text, "ONFLY_DB_PASSWORD") {
		t.Fatalf("workflow contains yourdomain.com\n%s", text)
	}
	before := append([]byte(nil), body...)
	if err := runInit(dir, false, true); err == nil {
		t.Fatal("expected existing workflow to fail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("workflow bytes changed")
	}

	dir = t.TempDir()
	onfly := filepath.Join(dir, ".onfly")
	if err := os.MkdirAll(onfly, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("project: keep\n")
	cfgPath := filepath.Join(onfly, "config.yaml")
	if err := os.WriteFile(cfgPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInit(dir, false, true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("config changed: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".github", "workflows", "onfly-index.yml")); err != nil {
		t.Fatal(err)
	}
}
