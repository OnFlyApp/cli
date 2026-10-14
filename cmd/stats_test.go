package cmd

import (
	"bytes"
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
