package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDetectLanguagesFromManifests(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "composer.json", "requirements.txt", "pyproject.toml"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := detectLanguages(dir); !reflect.DeepEqual(got, []string{"go", "php", "python"}) {
		t.Fatalf("%v", got)
	}
}

func TestEnsureGitignoreAppendsOnce(t *testing.T) {
	dir := t.TempDir()
	if added, err := ensureGitignore(dir); err != nil || added {
		t.Fatalf("no git repo and no .gitignore: %v %v", added, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		if added, err := ensureGitignore(dir); err != nil || added != want {
			t.Fatalf("call %d: %v %v", i, added, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(raw) != "bin\n.onfly/\n" {
		t.Fatalf("%q", raw)
	}
}

func TestBootstrapReportsFailedInstall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := installParser
	defer func() { installParser = old }()
	installParser = func(_ context.Context, _, name string) error { return errors.New("offline") }
	var out strings.Builder
	bootstrapProject(context.Background(), &out, dir)
	if !strings.Contains(out.String(), "try: onfly ext install go") {
		t.Fatalf("%s", out.String())
	}
}
