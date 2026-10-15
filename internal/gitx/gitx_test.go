package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestLineLogFollowsOneFunction(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.go")
	write := func(body string) {
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q")
	write("package a\n\nfunc One() int {\n\treturn 1\n}\n\nfunc Two() int {\n\treturn 2\n}\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "add both")
	write("package a\n\nfunc One() int {\n\treturn 11\n}\n\nfunc Two() int {\n\treturn 2\n}\n")
	git(t, dir, "commit", "-qam", "change one")
	ctx := context.Background()
	got, err := LineLog(ctx, dir, "a.go", 3, 5, 5)
	if err != nil || len(got) != 2 || got[0].Subject != "change one" || got[1].Subject != "add both" {
		t.Fatalf("One: %+v %v", got, err)
	}
	got, err = LineLog(ctx, dir, "a.go", 7, 9, 5)
	if err != nil || len(got) != 1 || got[0].Subject != "add both" {
		t.Fatalf("Two: %+v %v", got, err)
	}
	all, err := FileLog(ctx, dir, "a.go", 5)
	if err != nil || len(all) != 2 {
		t.Fatalf("file: %+v %v", all, err)
	}
	if _, err := LineLog(ctx, dir, "-x", 1, 2, 5); err == nil {
		t.Fatal("a file starting with - must be rejected")
	}
}
