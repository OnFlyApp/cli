package cmd

import (
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/index"
)

func TestFormatIndexProgress(t *testing.T) {
	plain := formatIndexProgress(index.Event{Action: "opening database"}, false)
	if plain != "opening database" {
		t.Fatalf("phase: %q", plain)
	}
	file := formatIndexProgress(index.Event{Action: "parsing", Path: "src/a.go", N: 2, Total: 4}, false)
	if file != "2/4 parsing src/a.go" {
		t.Fatalf("file: %q", file)
	}
	bar := formatIndexProgress(index.Event{Action: "embedding", Path: "src/a.go", N: 2, Total: 4}, true)
	if bar != "[##########----------] 2/4 embedding src/a.go" {
		t.Fatalf("bar: %q", bar)
	}
	long := formatIndexProgress(index.Event{Action: "parsing", Path: strings.Repeat("a", 120), N: 1, Total: 1}, true)
	if len(long) != 100 || !strings.HasSuffix(long, "...") {
		t.Fatalf("clip: %q len %d", long, len(long))
	}
}
