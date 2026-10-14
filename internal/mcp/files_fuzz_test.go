package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzSafePath(f *testing.F) {
	for _, s := range []string{"", ".", "a/b", "../x", "/etc/passwd", "a/../../x", "link", "a\x00b"} {
		f.Add(s)
	}
	root := f.TempDir()
	outside := f.TempDir()
	_ = os.Symlink(outside, filepath.Join(root, "link"))
	resolvedRoot, _ := filepath.EvalSymlinks(root)
	f.Fuzz(func(t *testing.T, req string) {
		got, err := SafePath(root, req)
		if err != nil {
			return
		}
		if !within(resolvedRoot, got) {
			t.Fatalf("SafePath(%q) = %q escapes root", req, got)
		}
	})
}
