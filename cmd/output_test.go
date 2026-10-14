package cmd

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
)

func TestPlainHandlerHasNoTimestampAndKeepsAttrs(t *testing.T) {
	var b bytes.Buffer
	l := slog.New(newPlainHandler(&b, slog.LevelInfo, false))
	l.Warn("parse", "path", "a.go", "err", "boom")
	l.Info("indexed a.go", "chunks", 3)
	l.Debug("hidden")
	want := "warning: parse path=a.go err=boom\nindexed a.go chunks=3\n"
	if b.String() != want {
		t.Fatalf("%q", b.String())
	}
}

func TestDBLabelHidesCredentials(t *testing.T) {
	got := dbLabel(config.Database{Driver: "postgres", DSN: "postgres://u:secret@db.internal:5432/onfly?sslmode=disable"})
	if got != "postgres db.internal:5432/onfly" || strings.Contains(got, "secret") {
		t.Fatalf("%q", got)
	}
	if got := dbLabel(config.Database{Driver: "sqlite", DSN: ".onfly/vector.db"}); got != "sqlite .onfly/vector.db" {
		t.Fatalf("%q", got)
	}
}

func TestInitReportListsFilesAndNextSteps(t *testing.T) {
	dir := t.TempDir()
	created, err := initProject(dir, false, false)
	if err != nil || len(created) != 3 {
		t.Fatalf("%v %v", created, err)
	}
	var b bytes.Buffer
	printInitReport(&b, created, false)
	for _, want := range []string{"created .onfly/config.yaml", "onfly index", "onfly mcp-config"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("missing %q in\n%s", want, b.String())
		}
	}
	_, err = initProject(dir, false, false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("the error must say how to overwrite: %v", err)
	}
}

func TestSnippetSkipsBlankLinesAndShortens(t *testing.T) {
	if got := snippet("\n\n  func A() {}\nmore"); got != "func A() {}" {
		t.Fatalf("%q", got)
	}
	if got := snippet(strings.Repeat("x", 300)); len([]rune(got)) != 100 {
		t.Fatalf("len %d", len([]rune(got)))
	}
	if snippet("  \n") != "" {
		t.Fatal("blank content has no snippet")
	}
}
