package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func lintProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".onfly/config.yaml", "project: demo\ndatabase:\n  driver: sqlite\n  dsn: .onfly/vector.db\nembedder:\n  provider: ollama\n  model: m\n  dimensions: 4\nrules:\n  - .onfly/rules/lint.md\nindexer:\n  include: [\"**/*\"]\nserver:\n  transport: stdio\n  log_file: .onfly/server.log\n")
	write(".onfly/rules/lint.md", "---\nname: No prints\nscope: [\"**/*.go\"]\nforbidden_calls: [fmt.Println]\nmessage: use the logger\n---\nLog with slog.\n")
	write("a.go", "package a\n\nfunc f() {}\n")
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	write("a.go", "package a\n\nimport \"fmt\"\n\nfunc f() {\n\tfmt.Println(\"x\")\n}\n")
	return dir
}

func runLint(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append([]string{"lint"}, args...))
	t.Cleanup(func() {
		lintBase, lintStaged, lintAll, lintFormat, lintStrict = "", false, false, "text", false
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return out.String(), err
}

func TestLintReportsAddedForbiddenCallsAndFailsTheRun(t *testing.T) {
	t.Chdir(lintProject(t))
	out, err := runLint(t)
	if err == nil || !strings.Contains(err.Error(), "1 errors") {
		t.Fatalf("an error-level violation fails the run: %v", err)
	}
	if want := "a.go:6: error: use the logger [No prints] fmt.Println("; !strings.Contains(out, want) {
		t.Fatalf("missing %q in\n%s", want, out)
	}
	jsonOut, _ := runLint(t, "--format", "json")
	var vs []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &vs); err != nil || len(vs) != 1 || vs[0]["line"].(float64) != 6 {
		t.Fatalf("%q %v", jsonOut, err)
	}
	if _, err := runLint(t, "--staged"); err != nil {
		t.Fatalf("nothing is staged, so nothing is checked: %v", err)
	}
	if _, err := runLint(t, "other/dir"); err != nil {
		t.Fatalf("a path filter that selects nothing passes: %v", err)
	}
	if _, err := runLint(t, "--all"); err == nil {
		t.Fatal("--all checks whole tracked files; a.go is tracked and now has the call")
	}
}
