package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestStdoutIsNeverALogDestination(t *testing.T) {
	if SafeWriter(os.Stdout) != os.Stderr {
		t.Fatal("standard output must become standard error")
	}
	// A duplicate of the stdout descriptor is stdout too.
	dup, err := os.OpenFile("/dev/stdout", os.O_WRONLY, 0)
	if err == nil {
		defer dup.Close()
		if _, ok := SafeWriter(dup).(*os.File); !ok {
			t.Fatal("a file writer stays a file")
		}
	}
	var b bytes.Buffer
	if SafeWriter(&b) != &b {
		t.Fatal("other writers are kept")
	}
	if SafeWriter(nil) == nil {
		t.Fatal("nil must not panic later")
	}
	// New must not hand a logger the real stdout.
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	New(w, Options{}).Info("must not appear on stdout")
	os.Stdout = old
	w.Close()
	var got bytes.Buffer
	got.ReadFrom(r)
	if got.Len() != 0 {
		t.Fatalf("a log line reached stdout: %q", got.String())
	}
}

func TestFormatsAndLevels(t *testing.T) {
	var b bytes.Buffer
	l := New(&b, Options{JSON: true})
	l.Debug("hidden")
	l.Info("shown", "path", "a.go")
	var rec map[string]any
	if err := json.Unmarshal(b.Bytes(), &rec); err != nil || rec["msg"] != "shown" || rec["path"] != "a.go" || strings.Contains(b.String(), "hidden") {
		t.Fatalf("%q %v", b.String(), err)
	}

	b.Reset()
	New(&b, Options{Verbose: true}).Debug("detail")
	if !strings.Contains(b.String(), "detail") {
		t.Fatalf("verbose shows debug: %q", b.String())
	}

	b.Reset()
	New(&b, Options{Plain: true}).Warn("careful", "path", "a.go")
	if b.String() != "warning: careful path=a.go\n" {
		t.Fatalf("%q", b.String())
	}

	b.Reset()
	New(&b, Options{Level: slog.LevelError}).Warn("quiet")
	if b.Len() != 0 {
		t.Fatalf("%q", b.String())
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "nonsense": slog.LevelInfo} {
		if got := ParseLevel(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}
