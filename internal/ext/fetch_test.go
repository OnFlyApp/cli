package ext

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadChecksumMismatchRemovesTemp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-wasm"))
	}))
	defer srv.Close()
	client := NewClient()
	client.Transport = srv.Client().Transport
	_, _, err := Download(context.Background(), client, srv.URL+"/php.wasm", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != ErrChecksumMismatch {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "onfly_download_") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestDownloadRejectsHTTPRedirectAndOversize(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redir":
			http.Redirect(w, r, "http://127.0.0.1/evil.wasm", http.StatusFound)
		case "/big":
			_, _ = w.Write([]byte("0123456789"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := NewClient()
	client.Transport = srv.Client().Transport
	_, _, err := Download(context.Background(), client, srv.URL+"/redir", "ab")
	if err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatal(err)
	}
	_, _, err = download(context.Background(), client, srv.URL+"/big", fmt.Sprintf("%x", sha256.Sum256([]byte("0123456789"))), 8)
	if err == nil || !strings.Contains(err.Error(), "64MiB") {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "onfly_download_") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestDownloadAndMove(t *testing.T) {
	body := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()
	client := NewClient()
	client.Transport = srv.Client().Transport
	temp, got, err := Download(context.Background(), client, srv.URL+"/php.wasm", strings.ToUpper(sum))
	if err != nil || got != sum {
		t.Fatal(err, got)
	}
	dest := filepath.Join(t.TempDir(), "plugins", "php-parser.wasm")
	if err := MoveInto(temp, dest); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dest)
	if err != nil || string(b) != string(body) {
		t.Fatalf("%v %q", err, b)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("temp still present", err)
	}
}
