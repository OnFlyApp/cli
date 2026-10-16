package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/telemetry"
	"github.com/axitdev/onfly/internal/vectorstore"
)

func TestFormatStats(t *testing.T) {
	var b bytes.Buffer
	formatStats(&b, vectorstore.IndexStats{
		Files: 2, Chunks: 3, Unnamed: 1,
		ByType:     map[string]int{"method": 1, "function": 2},
		Workspaces: map[string]int{"backend": 3},
	})
	want := "files: 2\nchunks: 3 (1 without metadata.name)\ntypes: function=2 method=1\nworkspaces: backend=3\n"
	if b.String() != want {
		t.Fatalf("%q", b.String())
	}
}

func TestIndexHintsFlagNoisyExtensions(t *testing.T) {
	s := vectorstore.IndexStats{ByExt: map[string]vectorstore.ExtStat{
		".go":   {Files: 10, Chunks: 50, Bytes: 600},
		".json": {Files: 3, Chunks: 9, Bytes: 400},
	}}
	hints := indexHints(s)
	if len(hints) != 1 || !strings.Contains(hints[0], ".json files are 40%") {
		t.Fatalf("%v", hints)
	}
	if top := topExtensions(s, 1); len(top) != 1 || top[0] != ".go 60% (10 files)" {
		t.Fatalf("%v", top)
	}
	s.ByExt[".json"] = vectorstore.ExtStat{Files: 1, Bytes: 10}
	if got := indexHints(s); len(got) != 0 {
		t.Fatalf("small share must not hint: %v", got)
	}
	if got := indexHints(vectorstore.IndexStats{}); len(got) != 0 {
		t.Fatalf("empty index: %v", got)
	}
}

func TestContextHints(t *testing.T) {
	var s vectorstore.IndexStats
	got := contextHints("nomic-embed-text", 0, s)
	if len(got) != 1 || !strings.Contains(got[0], "reads about 2048 tokens") {
		t.Fatalf("%v", got)
	}
	if got := contextHints("nomic-embed-text", 1800, s); len(got) != 0 {
		t.Fatalf("a max_input_tokens that fits needs no hint: %v", got)
	}
	if got := contextHints("embeddinggemma-2", 0, s); len(got) != 0 {
		t.Fatalf("a long-context model needs no hint: %v", got)
	}
	s.LongChunks, s.MaxChunkBytes = 3, 20000
	if got := contextHints("qwen3-embedding:0.6b", 6000, s); len(got) != 1 || !strings.Contains(got[0], "3 chunks are longer") {
		t.Fatalf("%v", got)
	}
}

func TestPrintTelemetrySummarisesSavings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.db")
	var empty strings.Builder
	if err := printTelemetry(context.Background(), &empty, path); err != nil || empty.Len() != 0 {
		t.Fatalf("no file, no output: %q %v", empty.String(), err)
	}
	tel, err := telemetry.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tel.Record(context.Background(), telemetry.Event{Tool: "onfly_xray", Target: "a.go", RawBytes: 40000, CompressedBytes: 4000, Rules: 2})
	tel.Close()
	var out strings.Builder
	if err := printTelemetry(context.Background(), &out, path); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"requests served: 1", "tokens saved: 9000 (90%", "usd saved: $0.03", "rules injected: 2"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in\n%s", want, out.String())
		}
	}
}
