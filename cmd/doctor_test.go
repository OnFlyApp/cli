package cmd

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	_ "modernc.org/sqlite"
)

func TestDoctorReportsEachCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"embeddings":[[1,0,0,1]]}`))
	}))
	defer srv.Close()

	missing := t.TempDir()
	writeDoctorProject(t, missing, srv.URL, ".onfly/vector.db", "plugins/missing.wasm")
	cfg := mustDoctorConfig(t, missing)
	var buf strings.Builder
	err := Doctor(context.Background(), cfg, &buf)
	out := buf.String()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, needle := range []string{"config: ok", "database:", "embedder: ok", "plugins:"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("missing %q in\n%s", needle, out)
		}
	}

	okDir := t.TempDir()
	writeDoctorProject(t, okDir, srv.URL, ".onfly/vector.db", "plugins/parser.wasm")
	cfg = mustDoctorConfig(t, okDir)
	if err := os.MkdirAll(filepath.Dir(cfg.Database.DSN), 0o755); err != nil {
		t.Fatal(err)
	}
	touchSQLite(t, cfg.Database.DSN)
	wasm := cfg.Indexer.Parsers[0].Path
	if err := os.MkdirAll(filepath.Dir(wasm), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("\x00asm-bytes")
	if err := os.WriteFile(wasm, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := Doctor(context.Background(), cfg, &buf); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	out = buf.String()
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])
	if !strings.Contains(out, "plugins: ok") || !strings.Contains(out, hexSum) {
		t.Fatalf("plugins line missing hash\n%s", out)
	}
	assertNoSchemaVersion(t, cfg.Database.DSN)
}

func writeDoctorProject(t *testing.T, dir, endpoint, dsn, wasm string) {
	t.Helper()
	onfly := filepath.Join(dir, ".onfly")
	if err := os.MkdirAll(onfly, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "project: demo\ndatabase:\n  driver: sqlite\n  dsn: " + dsn + "\nembedder:\n  provider: ollama\n  endpoint: " + endpoint + "\n  model: fake\n  dimensions: 4\nindexer:\n  include: [\"**/*\"]\n  parsers:\n    - extensions: [\".go\"]\n      type: wasm\n      path: " + wasm + "\nserver:\n  transport: stdio\n  log_file: .onfly/server.log\n"
	if err := os.WriteFile(filepath.Join(onfly, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustDoctorConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func touchSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorRulesWarningAndSyntax(t *testing.T) {
	dir := t.TempDir()
	writeRulesConfig(t, dir, 1)
	writeMD(t, dir, "one.md", "---\nname: One\nscope: [\"*\"]\npriority: 2\n---\n"+strings.Repeat("alpha ", 80))
	writeMD(t, dir, "two.md", "---\nname: Two\nscope: [\"*\"]\npriority: 1\n---\n"+strings.Repeat("beta ", 80))
	cfg := mustDoctorConfig(t, dir)
	var buf strings.Builder
	if err := checkRules(cfg, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[WARNING]") || !strings.Contains(buf.String(), "overlapping scopes") {
		t.Fatalf("%s", buf.String())
	}

	bad := t.TempDir()
	writeRulesConfig(t, bad, 15)
	writeMD(t, bad, "broken.md", "---\nscope: [\n")
	cfg = mustDoctorConfig(t, bad)
	buf.Reset()
	err := checkRules(cfg, &buf)
	if err == nil || !strings.Contains(buf.String(), "broken.md") {
		t.Fatalf("%v\n%s", err, buf.String())
	}
}

func writeRulesConfig(t *testing.T, dir string, percent int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".onfly", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("project: demo\ndatabase:\n  driver: sqlite\n  dsn: .onfly/vector.db\nembedder:\n  provider: ollama\n  endpoint: http://127.0.0.1:1\n  model: fake\n  dimensions: 4\nrules_engine:\n  max_token_budget_percent: %d\nindexer:\n  include: [\"**/*\"]\nserver:\n  transport: stdio\n  log_file: .onfly/server.log\n", percent)
	if err := os.WriteFile(filepath.Join(dir, ".onfly", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMD(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".onfly", "rules", name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertNoSchemaVersion(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	err = db.QueryRow(`SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&value)
	if err == nil {
		t.Fatalf("schema version = %s", value)
	}
}

func TestCheckReranker(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	var cfg config.Config
	var buf strings.Builder
	if err := checkReranker(cfg, &buf); err != nil || buf.Len() != 0 {
		t.Fatalf("%v %q", err, buf.String())
	}
	cfg.Reranker.Provider = "voyage"
	cfg.Reranker.Model = "rerank-2"
	if err := checkReranker(cfg, &buf); err == nil || !strings.HasPrefix(buf.String(), "reranker: ") || strings.Contains(buf.String(), "reranker: ok") {
		t.Fatalf("%v %q", err, buf.String())
	}
	buf.Reset()
	cfg.Reranker.APIKey = "k"
	if err := checkReranker(cfg, &buf); err != nil || buf.String() != "reranker: ok\n" {
		t.Fatalf("%v %q", err, buf.String())
	}
}

func TestDoctorRejectsAFileThatIsNotWebAssemblyAndNamesCustomTuning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"embeddings":[[1,0,0,1]]}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	writeDoctorProject(t, dir, srv.URL, ".onfly/vector.db", "plugins/parser.wasm")
	cfg := mustDoctorConfig(t, dir)
	wasm := cfg.Indexer.Parsers[0].Path
	if err := os.MkdirAll(filepath.Dir(wasm), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wasm, []byte("<?php not a module"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Xray.MaxHelpers = 1
	cfg.Indexer.Parsers[0].IgnoreDirectives = []string{"declare"}
	var buf strings.Builder
	if err := Doctor(context.Background(), cfg, &buf); err == nil {
		t.Fatal("a parser file that is not WebAssembly must fail the check")
	}
	out := buf.String()
	for _, want := range []string{"is not a WebAssembly module", "tuning: ok (custom: xray; parser rules"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

func TestDoctorReportsRuleCountFallbackAndProblems(t *testing.T) {
	dir := t.TempDir()
	writeDoctorProject(t, dir, "http://127.0.0.1:1", ".onfly/vector.db", "plugins/missing.wasm")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Use the repository layer."), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustDoctorConfig(t, dir)
	var buf strings.Builder
	if err := checkRules(cfg, &buf); err != nil || !strings.Contains(buf.String(), "rules: ok 1 rules, ") || !strings.Contains(buf.String(), "(auto-discovered AGENTS.md)") {
		t.Fatalf("%v %q", err, buf.String())
	}

	bad := t.TempDir()
	writeDoctorProject(t, bad, "http://127.0.0.1:1", ".onfly/vector.db", "plugins/missing.wasm")
	rules := filepath.Join(bad, ".onfly", "rules")
	if err := os.MkdirAll(rules, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(rules, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("glob.md", "---\nname: G\nscope: [\"src/[\"]\n---\nok\n")
	buf.Reset()
	if err := checkRules(mustDoctorConfig(t, bad), &buf); err != nil || !strings.Contains(buf.String(), "[WARNING] .onfly/rules/glob.md: scope glob \"src/[\" is not valid") {
		t.Fatalf("an invalid glob is a warning, not a failure: %v %q", err, buf.String())
	}
	write("template.md", "---\nname: T\n---\n{{ .Nope }}\n")
	write("yaml.md", "---\nname: [unclosed\n---\nbody\n")
	buf.Reset()
	if err := checkRules(mustDoctorConfig(t, bad), &buf); err == nil || !strings.Contains(buf.String(), "template.md") || !strings.Contains(buf.String(), "yaml.md") {
		t.Fatalf("a template or frontmatter error fails the check and names the file: %v %q", err, buf.String())
	}
}
