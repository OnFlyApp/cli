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
	payload := []byte("wasm-bytes")
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
