package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func TestPrintHitsPlain(t *testing.T) {
	var b bytes.Buffer
	err := printHits(&b, []parser.Chunk{
		{FilePath: "a.go", Content: "x", Metadata: map[string]string{"name": "Save", "receiver": "User", "start_line": "12", "end_line": "30"}},
		{FilePath: "b.go", Content: "y"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "a.go:12-30  User.Save\nb.go\n"
	if b.String() != want {
		t.Fatalf("%q", b.String())
	}
}

func TestPrintHitsJSONAndEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := printHits(&b, nil, true); err != nil || strings.TrimSpace(b.String()) != "[]" {
		t.Fatalf("%q %v", b.String(), err)
	}
	b.Reset()
	if err := printHits(&b, nil, false); err != nil || b.Len() != 0 {
		t.Fatalf("%q %v", b.String(), err)
	}
}

func TestCheckEvalK(t *testing.T) {
	for _, k := range []int{0, -1, 51} {
		if checkEvalK(k) == nil {
			t.Fatalf("k=%d accepted", k)
		}
	}
	if checkEvalK(1) != nil || checkEvalK(50) != nil {
		t.Fatal("bounds rejected")
	}
}
