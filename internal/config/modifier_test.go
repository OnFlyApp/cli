package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleYAML = `# keep me
project: "Demo"

database:
  driver: "sqlite"
  dsn: ".onfly/vector.db"

embedder:
  provider: "ollama"
  endpoint: "http://localhost:11434"
  model: "qwen3-embedding:0.6b"
  dimensions: 1024

indexer:
  include: ["src/**/*"]
  # parsers stay
  parsers:
    - extensions: [".md", ".blade.php"]
      type: "stdio"
      command: "php .onfly/plugins/custom_chunker.php"

server:
  transport: "stdio"
  log_level: "debug"
  log_file: ".onfly/server.log"
`

func writeSample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".onfly", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUpsertKeepsCommentsAndStdio(t *testing.T) {
	path := writeSample(t)
	err := UpsertWasmParser(path, WasmParser{
		Extensions: []string{".PHP", "inc", ".phtml"},
		Path:       `.onfly/plugins/php-parser.wasm`,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, needle := range []string{"# keep me", "# parsers stay", "custom_chunker.php", ".onfly/plugins/php-parser.wasm", `".php"`, `".inc"`, `".phtml"`} {
		if !strings.Contains(text, needle) {
			t.Fatalf("missing %s\n%s", needle, text)
		}
	}
	cfg, err := Load(ProjectRoot(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Indexer.Parsers) != 2 {
		t.Fatalf("%+v", cfg.Indexer.Parsers)
	}
}

func TestConflictLeavesBytesAndThenStrips(t *testing.T) {
	path := writeSample(t)
	old := WasmParser{Extensions: []string{".php"}, Path: ".onfly/plugins/old.wasm"}
	if err := UpsertWasmParser(path, old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	next := WasmParser{Extensions: []string{".php", ".phtml"}, Path: ".onfly/plugins/php-parser.wasm"}
	got, err := ParserConflicts(path, next)
	if err != nil || strings.Join(got, ",") != ".php" {
		t.Fatalf("%v %v", got, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("conflicts wrote the file")
	}
	if err := UpsertWasmParser(path, next); err != nil {
		t.Fatal(err)
	}
	text := string(mustRead(t, path))
	if strings.Contains(text, "old.wasm") {
		t.Fatalf("old entry remains\n%s", text)
	}
	if !strings.Contains(text, `".phtml"`) || !strings.Contains(text, "custom_chunker.php") {
		t.Fatalf("%s", text)
	}
}

func TestPartialOverlapKeepsOtherExtensions(t *testing.T) {
	path := writeSample(t)
	old := WasmParser{Extensions: []string{".php", ".inc"}, Path: ".onfly/plugins/old.wasm"}
	if err := UpsertWasmParser(path, old); err != nil {
		t.Fatal(err)
	}
	next := WasmParser{Extensions: []string{".php"}, Path: ".onfly/plugins/php-parser.wasm"}
	if err := UpsertWasmParser(path, next); err != nil {
		t.Fatal(err)
	}
	text := string(mustRead(t, path))
	if !strings.Contains(text, "old.wasm") || !strings.Contains(text, `".inc"`) {
		t.Fatalf("old .inc entry was dropped\n%s", text)
	}
}

func TestRemoveWasmIsIdempotent(t *testing.T) {
	path := writeSample(t)
	if err := UpsertWasmParser(path, WasmParser{Extensions: []string{".php"}, Path: ".onfly/plugins/php-parser.wasm"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWasmParser(path, ".onfly/plugins/php-parser.wasm"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWasmParser(path, ".onfly/plugins/php-parser.wasm"); err != nil {
		t.Fatal(err)
	}
	text := string(mustRead(t, path))
	if strings.Contains(text, "php-parser.wasm") || !strings.Contains(text, "# parsers stay") {
		t.Fatalf("%s", text)
	}
}

func TestNearestConfigPrefersChild(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, ".onfly", "config.yaml")
	childDir := filepath.Join(root, "svc")
	child := filepath.Join(childDir, ".onfly", "config.yaml")
	for _, p := range []string{parent, child} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("project: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NearestConfig(filepath.Join(childDir, "nested"))
	if err != nil || got != child {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := NearestConfig(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
