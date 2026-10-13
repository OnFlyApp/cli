package parser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureWASM(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fixture.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/fixture")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if buf, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, buf)
	}
	return out
}

func TestWasmParse(t *testing.T) {
	p, err := newWasmParser(context.Background(), fixtureWASM(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	chunks, err := p.Parse("src/A.php", []byte("<?php class A {}"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Metadata["name"] != "A" || chunks[0].FilePath != "src/A.php" {
		t.Fatalf("%+v", chunks)
	}
	if chunks[0].Content != "<?php class A {}" {
		t.Fatalf("content = %q", chunks[0].Content)
	}
}

func TestWasmStdoutCapIsAnError(t *testing.T) {
	p, err := newWasmParser(context.Background(), fixtureWASM(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	if _, err := p.Parse("a.go", []byte("BIG")); err == nil || !strings.Contains(err.Error(), "stdout exceeds") {
		t.Fatalf("%v", err)
	}
}

func TestWasmFailAndMissing(t *testing.T) {
	p, err := newWasmParser(context.Background(), fixtureWASM(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	if _, err := p.Parse("a.go", []byte("FAIL")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := newWasmParser(context.Background(), filepath.Join(t.TempDir(), "missing.wasm")); err == nil {
		t.Fatal("expected missing file error")
	}
	bad := filepath.Join(t.TempDir(), "bad.wasm")
	if err := os.WriteFile(bad, []byte("not wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newWasmParser(context.Background(), bad); err == nil {
		t.Fatal("expected compile error")
	}
}

func TestWasmTimeoutThenRecover(t *testing.T) {
	p, err := newWasmParser(context.Background(), fixtureWASM(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	start := time.Now()
	if _, err := p.Parse("a.go", []byte("LOOP")); err == nil || time.Since(start) > 8*time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
	chunks, err := p.Parse("a.go", []byte("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Metadata["name"] != "A" {
		t.Fatalf("%+v", chunks)
	}
}
