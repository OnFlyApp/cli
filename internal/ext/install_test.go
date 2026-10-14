package ext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/config"
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

func writeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".onfly", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func registryServer(t *testing.T, latest, old []byte, sumLatest, sumOld string) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry.json":
			fmt.Fprintf(w, `{
				"php": {
					"latest": "1.2.0",
					"versions": {
						"1.2.0": {"url": "https://%s/php.wasm", "sha256": %q, "extensions": [".php", ".inc", ".phtml"]},
						"1.0.0": {"url": "https://%s/old.wasm", "sha256": %q, "extensions": [".php"]}
					}
				}
			}`, r.Host, sumLatest, r.Host, sumOld)
		case "/php.wasm":
			_, _ = w.Write(latest)
		case "/old.wasm":
			_, _ = w.Write(old)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ONFLY_REGISTRY_URL", srv.URL+"/registry.json")
	client := NewClient()
	client.Transport = srv.Client().Transport
	return srv, client
}

func TestInstallRegistryWiresConfig(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	var wasmHits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry.json":
			fmt.Fprintf(w, `{
				"php": {"latest": "1.2.0", "versions": {
					"1.2.0": {"url": "https://%s/php.wasm", "sha256": %q, "extensions": [".php", ".inc", ".phtml"]}
				}}
			}`, r.Host, sum)
		case "/php.wasm":
			wasmHits++
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("ONFLY_REGISTRY_URL", srv.URL+"/registry.json")
	client := NewClient()
	client.Transport = srv.Client().Transport
	dir := writeProject(t)
	cfgPath := filepath.Join(dir, ".onfly", "config.yaml")
	if err := config.UpsertWasmParser(cfgPath, config.WasmParser{
		Extensions: []string{".php"},
		Path:       ".onfly/plugins/php-parser.wasm",
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opt := Options{Dir: dir, Stdout: &out, Client: client}
	if err := Install(context.Background(), opt, "php"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "installed php 1.2.0") {
		t.Fatal(out.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, ".onfly", "plugins", "php-parser.wasm"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("%v %q", err, got)
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var sawPHP, sawStdio bool
	for _, p := range cfg.Indexer.Parsers {
		if p.Type == "stdio" && strings.Contains(p.Command, "custom_chunker.php") {
			sawStdio = true
		}
		if p.Type == "wasm" && strings.HasSuffix(p.Path, "php-parser.wasm") {
			sawPHP = true
			joined := strings.Join(p.Extensions, ",")
			for _, ext := range []string{".php", ".inc", ".phtml"} {
				if !strings.Contains(joined, ext) {
					t.Fatalf("extensions %s", joined)
				}
			}
		}
	}
	if !sawPHP || !sawStdio {
		t.Fatalf("%+v", cfg.Indexer.Parsers)
	}
	lock, err := LoadLock(dir)
	if err != nil || lock["php"].Source != "registry" || lock["php"].Version != "1.2.0" {
		t.Fatalf("%v %+v", err, lock)
	}
	out.Reset()
	if err := Install(context.Background(), opt, "php"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "php 1.2.0 is current") {
		t.Fatal(out.String())
	}
	if wasmHits != 1 {
		t.Fatalf("wasm downloads %d", wasmHits)
	}
}

func TestInstallChecksumMismatchLeavesProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry.json":
			fmt.Fprintf(w, `{
				"php": {"latest": "1.2.0", "versions": {
					"1.2.0": {"url": "https://%s/php.wasm", "sha256": "%s", "extensions": [".php"]}
				}}
			}`, r.Host, strings.Repeat("a", 64))
		case "/php.wasm":
			_, _ = w.Write([]byte("not-wasm"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("ONFLY_REGISTRY_URL", srv.URL+"/registry.json")
	client := NewClient()
	client.Transport = srv.Client().Transport
	dir := writeProject(t)
	before, err := os.ReadFile(filepath.Join(dir, ".onfly", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	err = Install(context.Background(), Options{Dir: dir, Stdout: &bytes.Buffer{}, Client: client}, "php")
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".onfly", "config.yaml"))
	if string(before) != string(after) {
		t.Fatal("config changed")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".onfly", "plugins", "*.wasm"))
	if len(matches) != 0 {
		t.Fatal(matches)
	}
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "onfly_download_") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestInstallDeclineAndNonTTY(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	_, client := registryServer(t, body, []byte("old"), sum, sum)
	dir := writeProject(t)
	cfgPath := filepath.Join(dir, ".onfly", "config.yaml")
	if err := config.UpsertWasmParser(cfgPath, config.WasmParser{
		Extensions: []string{".php"}, Path: ".onfly/plugins/old.wasm",
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfgPath)
	err := Install(context.Background(), Options{Dir: dir, Stdout: &bytes.Buffer{}, Client: client}, "php")
	if err == nil || !strings.Contains(err.Error(), "rerun with --yes") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(cfgPath)
	if string(before) != string(after) {
		t.Fatal("config changed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".onfly", "plugins", "php-parser.wasm")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := Install(context.Background(), Options{Dir: dir, Yes: true, Stdout: &bytes.Buffer{}, Client: client}, "php"); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(text), "old.wasm") || !strings.Contains(string(text), "php-parser.wasm") || !strings.Contains(string(text), "custom_chunker.php") {
		t.Fatalf("%s", text)
	}
}

type newlineThenBlock struct{ gave bool }

func (r *newlineThenBlock) Read(p []byte) (int, error) {
	if !r.gave {
		r.gave = true
		if len(p) == 0 {
			return 0, nil
		}
		p[0] = '\n'
		return 1, nil
	}
	select {}
}

func TestInstallTTYEnterCancels(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	_, client := registryServer(t, body, body, sum, sum)
	dir := writeProject(t)
	cfgPath := filepath.Join(dir, ".onfly", "config.yaml")
	if err := config.UpsertWasmParser(cfgPath, config.WasmParser{
		Extensions: []string{".php"}, Path: ".onfly/plugins/old.wasm",
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfgPath)
	done := make(chan error, 1)
	go func() {
		done <- Install(context.Background(), Options{
			Dir: dir, IsTTY: true, Stdin: &newlineThenBlock{}, Stdout: &bytes.Buffer{}, Client: client,
		}, "php")
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "ext: install cancelled") {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt hung on enter")
	}
	after, _ := os.ReadFile(cfgPath)
	if string(before) != string(after) {
		t.Fatal("config changed")
	}
}

func TestInstallTTYYesReplaces(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	_, client := registryServer(t, body, body, sum, sum)
	dir := writeProject(t)
	cfgPath := filepath.Join(dir, ".onfly", "config.yaml")
	if err := config.UpsertWasmParser(cfgPath, config.WasmParser{
		Extensions: []string{".php"}, Path: ".onfly/plugins/old.wasm",
	}); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := Install(context.Background(), Options{
		Dir: dir, IsTTY: true, Stdin: strings.NewReader("y\n"), Stdout: out, Client: client,
	}, "php"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "installed php 1.2.0") {
		t.Fatal(out.String())
	}
	text, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(text), "old.wasm") || !strings.Contains(string(text), "php-parser.wasm") {
		t.Fatalf("%s", text)
	}
}

func TestInstallTTYDecline(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	_, client := registryServer(t, body, body, sum, sum)
	dir := writeProject(t)
	cfgPath := filepath.Join(dir, ".onfly", "config.yaml")
	if err := config.UpsertWasmParser(cfgPath, config.WasmParser{
		Extensions: []string{".php"}, Path: ".onfly/plugins/old.wasm",
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfgPath)
	err := Install(context.Background(), Options{
		Dir: dir, IsTTY: true, Stdin: strings.NewReader("n\n"), Stdout: &bytes.Buffer{}, Client: client,
	}, "php")
	if err == nil || !strings.Contains(err.Error(), "ext: install cancelled") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(cfgPath)
	if string(before) != string(after) {
		t.Fatal("config changed")
	}
}

func TestUpdateUpgradesAndSkipsLocal(t *testing.T) {
	oldBody := []byte("wasm-old")
	newBody := []byte("wasm-bytes")
	oldSum := fmt.Sprintf("%x", sha256.Sum256(oldBody))
	newSum := fmt.Sprintf("%x", sha256.Sum256(newBody))
	_, client := registryServer(t, newBody, oldBody, newSum, oldSum)
	dir := writeProject(t)
	cfgPath := filepath.Join(dir, ".onfly", "config.yaml")
	if err := config.UpsertWasmParser(cfgPath, config.WasmParser{
		Extensions: []string{".php"}, Path: ".onfly/plugins/php-parser.wasm",
	}); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(dir, ".onfly", "plugins")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "php-parser.wasm"), oldBody, 0o644); err != nil {
		t.Fatal(err)
	}
	localBody := []byte("local-wasm")
	if err := os.WriteFile(filepath.Join(pluginDir, "custom-parser.wasm"), localBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveLock(dir, map[string]Installed{
		"php":           {Version: "1.0.0", File: "php-parser.wasm", SHA256: oldSum, Source: "registry", Extensions: []string{".php"}},
		"custom-parser": {File: "custom-parser.wasm", SHA256: fmt.Sprintf("%x", sha256.Sum256(localBody)), Source: "local", Extensions: []string{".twig"}},
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opt := Options{Dir: dir, Stdout: &out, Client: client}
	if err := Update(context.Background(), opt, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "installed php 1.2.0") || !strings.Contains(out.String(), "skipped custom-parser (local)") {
		t.Fatal(out.String())
	}
	got, err := os.ReadFile(filepath.Join(pluginDir, "php-parser.wasm"))
	if err != nil || string(got) != string(newBody) {
		t.Fatalf("%v %q", err, got)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "custom-parser.wasm")); err != nil {
		t.Fatal(err)
	}
	if err := Update(context.Background(), opt, "custom-parser"); err == nil || !strings.Contains(err.Error(), "is a local plugin") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "custom-parser.wasm")); err != nil {
		t.Fatal(err)
	}
	if err := Update(context.Background(), opt, "missing"); err == nil || !strings.Contains(err.Error(), "is not installed") {
		t.Fatal(err)
	}
}

func TestRemoveStripsOnePlugin(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	_, client := registryServer(t, body, body, sum, sum)
	dir := writeProject(t)
	var out bytes.Buffer
	opt := Options{Dir: dir, Stdout: &out, Client: client}
	if err := Install(context.Background(), opt, "php"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Remove(opt, "php"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed php") {
		t.Fatal(out.String())
	}
	text, err := os.ReadFile(filepath.Join(dir, ".onfly", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), "php-parser.wasm") || !strings.Contains(string(text), "custom_chunker.php") {
		t.Fatalf("%s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, ".onfly", "plugins", "php-parser.wasm")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	lock, err := LoadLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lock["php"]; ok {
		t.Fatalf("still installed %+v", lock)
	}
	if err := Remove(opt, "php"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatal(err)
	}
}

func TestRemoveRejectsEscapingFile(t *testing.T) {
	dir := writeProject(t)
	outside := filepath.Join(dir, "outside.wasm")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveLock(dir, map[string]Installed{
		"php": {Version: "1.2.0", File: "../../outside.wasm", SHA256: "abc", Source: "registry", Extensions: []string{".php"}},
	}); err != nil {
		t.Fatal(err)
	}
	err := Remove(Options{Dir: dir, Stdout: &bytes.Buffer{}}, "php")
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("outside file: %v", statErr)
	}
	if err == nil || err.Error() != `ext: plugin "php" has an invalid file name` {
		t.Fatal(err)
	}
	lock, lerr := LoadLock(dir)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if _, ok := lock["php"]; !ok {
		t.Fatal("lock entry dropped")
	}
}

func TestListOfflineCorruptedAndOutdated(t *testing.T) {
	phpBody := []byte("php-wasm")
	phpSum := fmt.Sprintf("%x", sha256.Sum256(phpBody))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry.json" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{
			"php": {"latest": "1.3.0", "versions": {"1.3.0": {"url": "https://%s/php.wasm", "sha256": %q, "extensions": [".php"]}}},
			"go": {"latest": "1.3.0", "versions": {"1.3.0": {"url": "https://%s/go.wasm", "sha256": %q, "extensions": [".go"]}}}
		}`, r.Host, phpSum, r.Host, phpSum)
	}))
	defer srv.Close()
	t.Setenv("ONFLY_REGISTRY_URL", srv.URL+"/registry.json")
	client := NewClient()
	client.Transport = srv.Client().Transport
	dir := writeProject(t)
	pluginDir := filepath.Join(dir, ".onfly", "plugins")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "php-parser.wasm"), phpBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "go-parser.wasm"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveLock(dir, map[string]Installed{
		"php": {Version: "1.2.0", File: "php-parser.wasm", SHA256: phpSum, Source: "registry", Extensions: []string{".php"}},
		"go":  {Version: "1.2.0", File: "go-parser.wasm", SHA256: phpSum, Source: "registry", Extensions: []string{".go"}},
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opt := Options{Dir: dir, Stdout: &out, Client: client}
	if err := List(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	phpLine, goLine := pluginRow(out.String(), "php"), pluginRow(out.String(), "go")
	if !strings.Contains(phpLine, "outdated") {
		t.Fatalf("php: %q\n%s", phpLine, out.String())
	}
	if !strings.Contains(goLine, "corrupted") || strings.Contains(goLine, "outdated") {
		t.Fatalf("go: %q\n%s", goLine, out.String())
	}
	out.Reset()
	t.Setenv("ONFLY_REGISTRY_URL", "https://127.0.0.1:1/registry.json")
	opt.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("offline")
	})}
	if err := List(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	phpLine = pluginRow(text, "php")
	if !strings.Contains(phpLine, "active") || !strings.Contains(pluginRow(text, "go"), "corrupted") || !strings.Contains(text, "registry:") {
		t.Fatalf("%s", text)
	}
}

func TestListPrintsBeforeSlowRegistry(t *testing.T) {
	dir := writeProject(t)
	body := []byte("php-wasm")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	pluginDir := filepath.Join(dir, ".onfly", "plugins")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "php-parser.wasm"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveLock(dir, map[string]Installed{
		"php": {Version: "1.2.0", File: "php-parser.wasm", SHA256: sum, Source: "registry", Extensions: []string{".php"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONFLY_REGISTRY_URL", "https://registry.example/registry.json")
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
	}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- List(context.Background(), Options{Dir: dir, Stdout: &out, Client: client})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("list waited on the registry")
	}
	text := out.String()
	if !strings.Contains(pluginRow(text, "php"), "active") || !strings.Contains(text, "registry:") {
		t.Fatalf("%s", text)
	}
}

func TestListLocalActive(t *testing.T) {
	dir := writeProject(t)
	body := []byte("local-wasm")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	pluginDir := filepath.Join(dir, ".onfly", "plugins")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pluginDir, "custom-parser.wasm")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveLock(dir, map[string]Installed{
		"custom-parser": {File: "custom-parser.wasm", SHA256: sum, Source: "local", Extensions: []string{".twig"}},
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	t.Setenv("ONFLY_REGISTRY_URL", srv.URL+"/registry.json")
	client := NewClient()
	client.Transport = srv.Client().Transport
	var out bytes.Buffer
	opt := Options{Dir: dir, Stdout: &out, Client: client}
	if err := List(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	line := pluginRow(out.String(), "custom-parser")
	if !strings.Contains(line, "local") || !strings.Contains(line, "active") {
		t.Fatalf("%q\n%s", line, out.String())
	}
	if err := Update(context.Background(), opt, "custom-parser"); err == nil || !strings.Contains(err.Error(), "is a local plugin") {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func pluginRow(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			return line
		}
	}
	return ""
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInstallCurrentSkipsUnsetEnv(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	_, client := registryServer(t, body, body, sum, sum)
	dir := writeProject(t)
	path := filepath.Join(dir, ".onfly", "config.yaml")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text = bytes.ReplaceAll(text, []byte(`dsn: ".onfly/vector.db"`), []byte(`dsn: "postgres://u:${ONFLY_REVIEW_UNSET_DB}@localhost/db"`))
	if err := os.WriteFile(path, text, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opt := Options{Dir: dir, Stdout: &out, Client: client}
	if err := Install(context.Background(), opt, "php"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Install(context.Background(), opt, "php"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "php 1.2.0 is current") {
		t.Fatal(out.String())
	}
}

func TestRemoveRestoresWasmWhenLockSaveFails(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	_, client := registryServer(t, body, body, sum, sum)
	dir := writeProject(t)
	opt := Options{Dir: dir, Stdout: &bytes.Buffer{}, Client: client}
	if err := Install(context.Background(), opt, "php"); err != nil {
		t.Fatal(err)
	}
	wasm := filepath.Join(dir, ".onfly", "plugins", "php-parser.wasm")
	opt.SaveLock = func(string, map[string]Installed) error { return errors.New("disk") }
	if err := Remove(opt, "php"); err == nil {
		t.Fatal("expected lock save error")
	}
	got, err := os.ReadFile(wasm)
	if err != nil || string(got) != string(body) {
		t.Fatalf("%v %q", err, got)
	}
	text, err := os.ReadFile(filepath.Join(dir, ".onfly", "config.yaml"))
	if err != nil || !strings.Contains(string(text), "php-parser.wasm") {
		t.Fatalf("%v %s", err, text)
	}
	if _, err := os.Stat(wasm + ".removing"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestInstallLocalDoesNotDial(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("dialed")
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("ONFLY_REGISTRY_URL", srv.URL+"/registry.json")
	client := NewClient()
	client.Transport = srv.Client().Transport
	dir := writeProject(t)
	src := filepath.Join(dir, "custom-parser.wasm")
	if err := os.WriteFile(src, []byte("local-wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Install(context.Background(), Options{
		Dir: dir, Extensions: ".twig,.html", Stdout: &out, Client: client,
	}, src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "installed custom-parser local") {
		t.Fatal(out.String())
	}
	lock, err := LoadLock(dir)
	if err != nil || lock["custom-parser"].Source != "local" || lock["custom-parser"].Version != "" {
		t.Fatalf("%v %+v", err, lock)
	}
	text, _ := os.ReadFile(filepath.Join(dir, ".onfly", "config.yaml"))
	if !strings.Contains(string(text), `".twig"`) || !strings.Contains(string(text), `".html"`) || !strings.Contains(string(text), ".onfly/plugins/custom-parser.wasm") {
		t.Fatalf("%s", text)
	}
	err = Install(context.Background(), Options{
		Dir: dir, Extensions: ".twig", Stdout: &bytes.Buffer{}, Client: client,
	}, "php")
	if err == nil || !strings.Contains(err.Error(), "--extensions is only valid") {
		t.Fatal(err)
	}
}
