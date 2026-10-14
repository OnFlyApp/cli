package index

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

func TestFillChunkLines(t *testing.T) {
	file := []byte("package p\n\n\nfunc getUserId() {}\n\nfunc getUserId() {}\n")
	chunks := []parser.Chunk{
		{Content: "func getUserId() {}", Metadata: map[string]string{"name": "getUserId"}},
		{Content: "not in file", Metadata: map[string]string{}},
		{Content: "func getUserId() {}", Metadata: map[string]string{"start_line": "9", "end_line": "11"}},
	}
	fillChunkLines(file, chunks)
	if chunks[0].Metadata["start_line"] != "4" || chunks[0].Metadata["end_line"] != "4" {
		t.Fatalf("%v", chunks[0].Metadata)
	}
	if _, ok := chunks[1].Metadata["start_line"]; ok {
		t.Fatalf("%v", chunks[1].Metadata)
	}
	if chunks[2].Metadata["start_line"] != "9" || chunks[2].Metadata["end_line"] != "11" {
		t.Fatalf("%v", chunks[2].Metadata)
	}
}

func TestProgressNamesTheSlowSteps(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	body := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId() {}\",\"metadata\":{\"name\":\"getUserId\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "user.go"), []byte("package p\nfunc getUserId() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, dir)
	var got []string
	opt := Options{Progress: func(ev Event) {
		got = append(got, ev.Action+" "+ev.Path)
	}}
	if _, err := Run(context.Background(), cfg, nil, opt); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"loading tokenizer ",
		"opening database ",
		"loading parsers ",
		"walking ",
		"parsing src/user.go",
		"embedding src/user.go",
		"pruning ",
		"linking references ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	got = nil
	if _, err := Run(context.Background(), cfg, nil, opt); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\n") != "loading tokenizer \nopening database \nloading parsers \nwalking \nunchanged src/user.go\npruning \nlinking references " {
		t.Fatalf("second run\n%s", strings.Join(got, "\n"))
	}
}

func TestIndexStoresStartLine(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	body := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId() {}\",\"metadata\":{\"name\":\"getUserId\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "user.go"), []byte("package p\n\n\nfunc getUserId() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, dir)
	if _, err := Run(context.Background(), cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	store := openStore(t, cfg)
	defer store.Close()
	got, err := store.GetComponentAST(context.Background(), "getUserId")
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["start_line"] != "4" || got.Metadata["end_line"] != "4" || got.Content != "func getUserId() {}" {
		t.Fatalf("%+v", got)
	}
}

func TestSpecGlobs(t *testing.T) {
	ok, err := matchAny("src/User.go", []string{"src/**/*"})
	if err != nil || !ok {
		t.Fatalf("src/User.go: %v %v", ok, err)
	}
	ok, err = matchAny("src/a/b.go", []string{"src/**/*"})
	if err != nil || !ok {
		t.Fatalf("nested: %v %v", ok, err)
	}
	ok, err = matchAny("docs/a.md", []string{"docs/**/*.md"})
	if err != nil || !ok {
		t.Fatalf("docs/a.md: %v %v", ok, err)
	}
	ok, err = matchAny("docs/a/b.md", []string{"docs/**/*.md"})
	if err != nil || !ok {
		t.Fatalf("docs nested: %v %v", ok, err)
	}
	ok, err = matchAny("docs/a.txt", []string{"docs/**/*.md"})
	if err != nil || ok {
		t.Fatalf("txt should miss: %v %v", ok, err)
	}
}

func TestReindexPrunesAndSkips(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	seen := filepath.Join(dir, "seen.txt")
	scriptBody := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shellQuote(seen) + "\nif printf '%s' \"$(cat)\" | grep -q FAIL; then echo boom >&2; exit 1; fi\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId\",\"metadata\":{\"name\":\"User\"}}]'\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "user.go"), []byte("package user\nfunc getUserId() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "vendor", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "lib", "skip.go"), []byte("package skip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "note.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "src", "big.go"), big, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := mustLoad(t, dir)
	ctx := context.Background()
	stats, err := Run(ctx, cfg, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 || stats.Chunks != 1 || stats.Failed != 0 {
		t.Fatalf("stats %+v", stats)
	}
	seenBody, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(seenBody), "note.php") || strings.Contains(string(seenBody), "big.go") || strings.Contains(string(seenBody), "vendor") {
		t.Fatalf("parser saw skipped files:\n%s", seenBody)
	}

	stats, err = Run(ctx, cfg, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 1 || stats.Chunks != 0 || stats.Pruned != 0 {
		t.Fatalf("reindex %+v", stats)
	}
	seenAgain, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(seenAgain) != string(seenBody) {
		t.Fatalf("parser ran again:\n%s", seenAgain)
	}
	store := openStore(t, cfg)
	got, err := store.SearchKeyword(ctx, "getuserid", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Metadata["name"] != "User" {
		t.Fatalf("%+v", got)
	}
	store.Close()

	if err := os.Remove(filepath.Join(dir, "src", "user.go")); err != nil {
		t.Fatal(err)
	}
	stats, err = Run(ctx, cfg, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 0 || stats.Pruned != 1 {
		t.Fatalf("prune %+v", stats)
	}
	store = openStore(t, cfg)
	defer store.Close()
	if _, err := store.GetComponentAST(ctx, "User"); err != vectorstore.ErrNotFound {
		t.Fatalf("got %v", err)
	}
}

func TestFailedReindexKeepsChunks(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	body := "#!/bin/sh\nif printf '%s' \"$(cat)\" | grep -q FAIL; then echo boom >&2; exit 1; fi\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId\",\"metadata\":{\"name\":\"User\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "src", "user.go")
	if err := os.WriteFile(path, []byte("package user\nfunc getUserId() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, dir)
	ctx := context.Background()
	if _, err := Run(ctx, cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("FAIL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := Run(ctx, cfg, nil, Options{})
	if err == nil || stats.Failed != 1 {
		t.Fatalf("stats %+v err %v", stats, err)
	}
	store := openStore(t, cfg)
	if _, err := store.GetComponentAST(ctx, "User"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	stats, err = Run(ctx, cfg, nil, Options{})
	if err == nil || stats.Failed != 1 {
		t.Fatalf("third %+v err %v", stats, err)
	}
	store = openStore(t, cfg)
	defer store.Close()
	if _, err := store.GetComponentAST(ctx, "User"); err != nil {
		t.Fatal(err)
	}
}

func TestGitignoreSkipsEnv(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	seen := filepath.Join(dir, "seen.txt")
	scriptBody := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shellQuote(seen) + "\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId\",\"metadata\":{\"name\":\"User\"}}]'\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", ".env"), []byte("TOKEN=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "secret.go"), []byte("package secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "ok.go"), []byte("package ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, dir)
	if _, err := Run(context.Background(), cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	seenBody, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(seenBody), ".env") || strings.Contains(string(seenBody), "secret.go") {
		t.Fatalf("parser saw ignored files:\n%s", seenBody)
	}
}

func TestWithinRootResolvesAncestorSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.go")
	if err := os.WriteFile(real, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.go")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if !withinRoot(dir, target) {
		t.Fatalf("root %s target %s", dir, target)
	}
}

func TestSymlinkToIgnoredTargetIsNotRead(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	seen := filepath.Join(dir, "seen.txt")
	scriptBody := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> " + shellQuote(seen) + "\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId\",\"metadata\":{\"name\":\"User\"}}]'\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "ok.go"), []byte("package ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("TOKEN=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("hidden.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hidden.go"), []byte("package hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "..", "outside.go")
	outside, err := filepath.Abs(outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("package out\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })
	if err := os.Symlink(filepath.Join(src, "ok.go"), filepath.Join(src, "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, ".env"), filepath.Join(src, "envlink.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "hidden.go"), filepath.Join(src, "hidelink.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "outlink.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), mustLoad(t, dir), nil, Options{}); err != nil {
		t.Fatal(err)
	}
	seenBody, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	text := string(seenBody)
	if !strings.Contains(text, "link.go") || !strings.Contains(text, "ok.go") {
		t.Fatalf("missing in-root symlink:\n%s", text)
	}
	if strings.Contains(text, "envlink.go") || strings.Contains(text, "hidelink.go") || strings.Contains(text, "outlink.go") || strings.Contains(text, ".env") {
		t.Fatalf("parser saw blocked symlink:\n%s", text)
	}
}

func TestDropAbsentRemovesDeletedFile(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	body := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId\",\"metadata\":{\"name\":\"User\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "ok.go"), []byte("package ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, dir)
	if _, err := Run(context.Background(), cfg, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "src", "ok.go")); err != nil {
		t.Fatal(err)
	}
	session, err := Open(context.Background(), cfg, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.DropAbsent(context.Background()); err != nil {
		session.Close()
		t.Fatal(err)
	}
	session.Close()
	store := openStore(t, cfg)
	defer store.Close()
	files, err := store.ListFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file == "src/ok.go" {
			t.Fatal("deleted file still stored")
		}
	}
}

func TestIndexContinuesAfterFileError(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	body := "#!/bin/sh\nif printf '%s' \"$(cat)\" | grep -q FAIL; then echo boom >&2; exit 1; fi\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId\",\"metadata\":{\"name\":\"User\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "bad.go"), []byte("FAIL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "ok.go"), []byte("package ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := mustLoad(t, dir)
	stats, err := Run(context.Background(), cfg, nil, Options{})
	if err == nil || stats.Failed != 1 || stats.Files != 1 {
		t.Fatalf("stats %+v err %v", stats, err)
	}
	store := openStore(t, cfg)
	defer store.Close()
	got, err := store.GetComponentAST(context.Background(), "User")
	if err != nil || got.FilePath != "src/ok.go" {
		t.Fatalf("%v %+v", err, got)
	}
}

func fakeOllama(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vecs := make([][]float32, len(req.Input))
		for i, text := range req.Input {
			vecs[i] = []float32{1, 0, 0, float32(len(text)%50) + 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
}

func writeProject(t *testing.T, dir, endpoint, script string) {
	t.Helper()
	onfly := filepath.Join(dir, ".onfly")
	if err := os.MkdirAll(filepath.Join(onfly, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onfly, "rules", "architecture.md"), []byte("# Architecture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "project: demo\ndatabase:\n  driver: sqlite\n  dsn: .onfly/vector.db\nembedder:\n  provider: ollama\n  endpoint: " + endpoint + "\n  model: fake\n  dimensions: 4\nrules:\n  - .onfly/rules/architecture.md\nindexer:\n  include: [\"**/*\"]\n  exclude: [\"vendor\", \"node_modules\", \"var\", \"public\"]\n  parsers:\n    - extensions: [\".go\"]\n      type: stdio\n      command: " + shellQuote(script) + "\nserver:\n  transport: stdio\n  log_level: info\n  log_file: .onfly/server.log\n"
	if err := os.WriteFile(filepath.Join(onfly, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T, dir string) config.Config {
	t.Helper()
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func openStore(t *testing.T, cfg config.Config) vectorstore.Store {
	t.Helper()
	store, err := vectorstore.New(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(context.Background(), cfg.Embedder.Dimensions); err != nil {
		t.Fatal(err)
	}
	return store
}

func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func TestDryRunDoesNotCreateDB(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	body := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId\",\"metadata\":{\"name\":\"User\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, "http://127.0.0.1:1", script)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "user.go"), []byte("package user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := Run(context.Background(), mustLoad(t, dir), nil, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 || stats.Chunks != 1 {
		t.Fatalf("%+v", stats)
	}
	if _, err := os.Stat(filepath.Join(dir, ".onfly", "vector.db")); !os.IsNotExist(err) {
		t.Fatalf("db stat: %v", err)
	}
}

func TestCIBatch(t *testing.T) {
	parts := make([]string, 40)
	for i := range parts {
		parts[i] = `{"type":"function","content":"chunk","metadata":{"name":"C"}}`
	}
	scriptBody := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '[" + strings.Join(parts, ",") + "]'\n"
	run := func(ci bool) int {
		t.Helper()
		var posts int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			posts++
			vecs := make([][]float32, len(req.Input))
			for i := range vecs {
				vecs[i] = []float32{1, 0, 0, 1}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
		}))
		defer srv.Close()
		dir := t.TempDir()
		script := filepath.Join(dir, "chunk.sh")
		if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
			t.Fatal(err)
		}
		writeProject(t, dir, srv.URL, script)
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "user.go"), []byte("package user\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stats, err := Run(context.Background(), mustLoad(t, dir), nil, Options{CI: ci})
		if err != nil {
			t.Fatal(err)
		}
		if stats.Chunks != 40 {
			t.Fatalf("chunks %+v", stats)
		}
		return posts
	}
	if posts := run(false); posts != 2 {
		t.Fatalf("default posts = %d", posts)
	}
	if posts := run(true); posts != 1 {
		t.Fatalf("ci posts = %d", posts)
	}
}

func TestFillChunkLinesRepeatedSnippets(t *testing.T) {
	file := []byte("a\nx()\nb\nx()\n")
	chunks := []parser.Chunk{{Content: "x()"}, {Content: "x()"}}
	fillChunkLines(file, chunks)
	if chunks[0].Metadata["start_line"] != "2" || chunks[1].Metadata["start_line"] != "4" {
		t.Fatalf("%v %v", chunks[0].Metadata, chunks[1].Metadata)
	}
}

func TestConcurrentIndexMatchesSequential(t *testing.T) {
	srv := fakeOllama(t)
	defer srv.Close()
	dir := t.TempDir()
	script := filepath.Join(dir, "chunk.sh")
	body := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func f\",\"metadata\":{\"name\":\"F\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, dir, srv.URL, script)
	for i := 0; i < 12; i++ {
		name := filepath.Join(dir, "src", "f"+strconv.Itoa(i)+".go")
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("package f"+strconv.Itoa(i)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := mustLoad(t, dir)
	cfg.Indexer.Concurrency = 4
	stats, err := Run(context.Background(), cfg, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 12 || stats.Chunks != 12 || stats.Failed != 0 {
		t.Fatalf("stats %+v", stats)
	}
	again, err := Run(context.Background(), cfg, nil, Options{})
	if err != nil || again.Skipped != 12 {
		t.Fatalf("second run %+v %v", again, err)
	}
}
