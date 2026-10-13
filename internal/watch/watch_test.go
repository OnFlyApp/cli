package watch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/config"
)

func TestCoalesceOnce(t *testing.T) {
	got := make(chan string, 4)
	c := newCoalescer(40*time.Millisecond, func(p string) { got <- p })
	c.push("src/user.go")
	time.Sleep(10 * time.Millisecond)
	c.push("src/user.go")
	select {
	case p := <-got:
		if p != "src/user.go" {
			t.Fatalf("path = %s", p)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	}
	select {
	case p := <-got:
		t.Fatalf("second fire %s", p)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestWatchIgnoresOnflyDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[1,0,0,1]]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	seen := filepath.Join(dir, "seen.txt")
	script := filepath.Join(dir, "chunk.sh")
	body := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' \"$1\" >> " + quote(seen) + "\nprintf '%s\\n' '[{\"type\":\"function\",\"content\":\"func getUserId\",\"metadata\":{\"name\":\"User\"}}]'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	onfly := filepath.Join(dir, ".onfly")
	if err := os.MkdirAll(filepath.Join(onfly, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onfly, "rules", "architecture.md"), []byte("# Architecture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "project: demo\ndatabase:\n  driver: sqlite\n  dsn: .onfly/vector.db\nembedder:\n  provider: ollama\n  endpoint: " + srv.URL + "\n  model: fake\n  dimensions: 4\nrules:\n  - .onfly/rules/architecture.md\nindexer:\n  include: [\"**/*\"]\n  parsers:\n    - extensions: [\".go\"]\n      type: stdio\n      command: " + quote(script) + "\nserver:\n  transport: stdio\n  log_file: .onfly/server.log\n"
	if err := os.WriteFile(filepath.Join(onfly, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Indexer.DebounceMS = 50

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, nil) }()
	select {
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(time.Second):
	}

	user := filepath.Join(dir, "src", "user.go")
	if err := os.WriteFile(user, []byte("package user\nfunc getUserId() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(user, []byte("package user\nfunc getUserId() { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var seenBody []byte
	for {
		seenBody, _ = os.ReadFile(seen)
		if countLines(seenBody) >= 1 {
			break
		}
		select {
		case err := <-errCh:
			t.Fatal(err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("seen %q", seenBody)
		}
		time.Sleep(15 * time.Millisecond)
	}
	if n := countLines(seenBody); n != 1 {
		t.Fatalf("lines = %d\n%s", n, seenBody)
	}

	if err := os.WriteFile(filepath.Join(onfly, "vector.db"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", ".env"), []byte("TOKEN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	seenBody, err = os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if n := countLines(seenBody); n != 1 {
		t.Fatalf("lines = %d\n%s", n, seenBody)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func countLines(b []byte) int {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func quote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}
