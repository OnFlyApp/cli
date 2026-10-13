package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSafePathBlocksEscape(t *testing.T) {
	base := t.TempDir()
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "proj")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("yes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	got, err := SafePath(root, "ok.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(got)
	if err != nil || string(body) != "yes" {
		t.Fatalf("%v %q", err, body)
	}
	if _, err := SafePath(root, "leak"); err == nil {
		t.Fatal("symlink escape")
	}
	if _, err := SafePath(root, "../secret.txt"); err == nil {
		t.Fatal("path traversal")
	}

	h := &tools{root: root}
	_, out, err := h.readFile(context.Background(), nil, pathIn{Path: "ok.txt"})
	if err != nil || out.Content != "yes" {
		t.Fatalf("%v %+v", err, out)
	}
	if _, _, err := h.readFile(context.Background(), nil, pathIn{Path: "leak"}); err == nil {
		t.Fatal("read symlink")
	}
	_, entries, err := h.listDir(context.Background(), nil, pathIn{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "leak" || entries[1].Name != "ok.txt" || entries[1].Kind != "file" {
		t.Fatalf("%+v", entries)
	}
}

func TestReadFileRejectsSecretCase(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &tools{root: root}
	if _, _, err := h.readFile(context.Background(), nil, pathIn{Path: ".ENV"}); err == nil {
		t.Fatal("read .ENV")
	}
	if _, err := h.listDirBody(pathIn{Path: "Vendor"}); err == nil {
		t.Fatal("list Vendor")
	}
}

func TestReadFileCap(t *testing.T) {
	root := t.TempDir()
	body := make([]byte, maxRawBytes+1)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &tools{root: root}
	if _, _, err := h.readFile(context.Background(), nil, pathIn{Path: "big.txt"}); err == nil {
		t.Fatal("expected cap")
	}
}
