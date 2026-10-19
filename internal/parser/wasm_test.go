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
	chunks, err := p.Parse(context.Background(), "src/A.php", []byte("<?php class A {}"))
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
	if _, err := p.Parse(context.Background(), "a.go", []byte("BIG")); err == nil || !strings.Contains(err.Error(), "stdout exceeds") {
		t.Fatalf("%v", err)
	}
}

func TestWasmFailAndMissing(t *testing.T) {
	p, err := newWasmParser(context.Background(), fixtureWASM(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	if _, err := p.Parse(context.Background(), "a.go", []byte("FAIL")); err == nil {
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
	if _, err := p.Parse(context.Background(), "a.go", []byte("LOOP")); err == nil || time.Since(start) > 8*time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
	chunks, err := p.Parse(context.Background(), "a.go", []byte("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].Metadata["name"] != "A" {
		t.Fatalf("%+v", chunks)
	}
}

func TestWasmTimeoutGrowsWithTheFileUnlessConfigured(t *testing.T) {
	p := &WasmParser{}
	if p.timeoutFor(0) != parseTimeout || p.timeoutFor(80<<10) <= p.timeoutFor(10<<10) {
		t.Fatalf("%v %v", p.timeoutFor(0), p.timeoutFor(80<<10))
	}
	p.Timeout = 2 * time.Second
	if p.timeoutFor(80<<10) != 2*time.Second {
		t.Fatal("a configured timeout is used as given")
	}
}

func batchWASM(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "batch.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/batchfixture")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if buf, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build batch fixture: %v\n%s", err, buf)
	}
	return out
}

func TestWasmBatchServesManyFilesFromOneProcess(t *testing.T) {
	p, err := newWasmParser(context.Background(), batchWASM(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	if !p.batch.Load() {
		t.Fatal("a plugin that carries the batch marker must run in batch mode")
	}
	for i, want := range []string{"1", "2", "3"} {
		chunks, err := p.Parse(context.Background(), "a.php", []byte("<?php"))
		if err != nil || len(chunks) != 1 || chunks[0].Metadata["name"] != want || chunks[0].FilePath != "a.php" {
			t.Fatalf("%d: %+v %v", i, chunks, err)
		}
	}
	if _, err := p.Parse(context.Background(), "b.php", []byte("FAIL")); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a parse error comes back as an error: %v", err)
	}
	if chunks, err := p.Parse(context.Background(), "c.php", []byte("x")); err != nil || chunks[0].Metadata["name"] != "5" {
		t.Fatalf("the process survives a parse error: %+v %v", chunks, err)
	}
}

func TestWasmBatchTimeoutReplacesTheProcess(t *testing.T) {
	p, err := newWasmParser(context.Background(), batchWASM(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	p.Timeout = 300 * time.Millisecond
	if _, err := p.Parse(context.Background(), "a.php", []byte("LOOP")); err == nil {
		t.Fatal("expected a timeout")
	}
	chunks, err := p.Parse(context.Background(), "a.php", []byte("x"))
	if err != nil || chunks[0].Metadata["name"] != "1" {
		t.Fatalf("a new process answers after a timeout: %+v %v", chunks, err)
	}
}

func TestWasmBatchProcessDeathDoesNotStopTheNextFile(t *testing.T) {
	p, err := newWasmParser(context.Background(), batchWASM(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	_, _ = p.Parse(context.Background(), "a.php", []byte("DIE"))
	if !p.batch.Load() {
		t.Fatal("one dead process must not turn batch mode off")
	}
	chunks, err := p.Parse(context.Background(), "a.php", []byte("x"))
	if err != nil || chunks[0].Metadata["name"] != "1" {
		t.Fatalf("%+v %v", chunks, err)
	}
}
