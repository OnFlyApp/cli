package cmd

import (
	"bytes"
	"strings"
	"testing"

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
