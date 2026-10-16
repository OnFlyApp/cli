package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuideWriteIsIdempotent(t *testing.T) {
	f := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(f, []byte("# Mine\n\nkeep this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := writeGuide(f); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(f)
	s := string(b)
	if strings.Count(s, guideStart) != 1 || !strings.Contains(s, "keep this") || !strings.HasPrefix(s, "# Mine") || !strings.Contains(s, "onfly_xray") {
		t.Fatalf("%s", s)
	}
}

func TestGuideWriteCreatesFileAndReplacesOldBlock(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "new.md")
	if err := writeGuide(fresh); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(fresh); !strings.HasPrefix(string(b), guideStart) {
		t.Fatalf("%s", b)
	}
	f := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(f, []byte("a\n"+guideStart+"\nold\n"+guideEnd+"\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeGuide(f); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	if strings.Contains(string(b), "old") || !strings.HasPrefix(string(b), "a\n") || !strings.HasSuffix(string(b), "b\n") {
		t.Fatalf("%s", b)
	}
}
