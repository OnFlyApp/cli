package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSplitAndAllowed(t *testing.T) {
	w, err := Split(`go test -run "TestA|TestB" ./pkg/...`)
	if err != nil || !reflect.DeepEqual(w, []string{"go", "test", "-run", "TestA|TestB", "./pkg/..."}) {
		t.Fatalf("a quoted word is one word, operators inside quotes are literal: %v %v", w, err)
	}
	for _, bad := range []string{"go test ./... | tee out", "go test; rm -rf /", "go test && make", "echo $(whoami)", "echo `id`", "cat < /etc/passwd", "", "go test 'oops"} {
		if _, err := Split(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	allow := []string{"go test", "vendor/bin/phpunit"}
	for cmd, want := range map[string]bool{
		"go test ./...": true, "go test": true, "go build ./...": false, "gotest ./...": false,
		"vendor/bin/phpunit --filter X": true, "phpunit": false, "go": false,
	} {
		w, _ := Split(cmd)
		if got := Allowed(w, allow); got != want {
			t.Errorf("%q: %v, want %v", cmd, got, want)
		}
	}
}

func TestNothingRunsWithoutAnAllowlist(t *testing.T) {
	if _, err := Run(context.Background(), t.TempDir(), []string{"echo", "hi"}, nil, 0); err == nil || !strings.Contains(err.Error(), "run.commands") {
		t.Fatalf("%v", err)
	}
	if _, err := Run(context.Background(), t.TempDir(), []string{"rm", "-rf", "x"}, []string{"go test"}, 0); err == nil || !strings.Contains(err.Error(), "not in run.commands") {
		t.Fatalf("%v", err)
	}
}

func goProject(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs the go tool")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/demo\n\ngo 1.21\n")
	write("calc/calc.go", "package calc\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n")
	write("calc/calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Errorf(\"Add(2, 3) = %d, want 5\", got)\n\t}\n}\n\nfunc TestOK(t *testing.T) {}\n")
	write("good/good.go", "package good\n\nfunc One() int { return 1 }\n")
	write("good/good_test.go", "package good\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	return root
}

func TestGoTestFailuresBecomeMessagesAndProjectFrames(t *testing.T) {
	root := goProject(t)
	res, err := Run(context.Background(), root, []string{"go", "test", "./..."}, []string{"go test"}, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit == 0 || len(res.Failures) != 1 {
		t.Fatalf("one test failed: exit %d, %+v", res.Exit, res.Failures)
	}
	f := res.Failures[0]
	if f.Name != "TestAdd" || f.Package != "calc" || !strings.Contains(f.Message, "Add(2, 3) = -1, want 5") {
		t.Fatalf("%+v", f)
	}
	if len(f.Frames) != 1 || f.Frames[0] != (Frame{"calc/calc_test.go", 7}) {
		t.Fatalf("a bare file name in go test output is the package's file: %+v", f.Frames)
	}
	if !strings.HasPrefix(res.Summary, "1 failed, 2 passed") {
		t.Fatalf("%q", res.Summary)
	}
	if strings.Contains(f.Message, "=== RUN") || strings.Contains(f.Message, "--- FAIL: TestOK") {
		t.Fatalf("progress lines are dropped: %q", f.Message)
	}
}

func TestPassingRunIsOneLine(t *testing.T) {
	root := goProject(t)
	res, err := Run(context.Background(), root, []string{"go", "test", "./good"}, []string{"go test"}, 60*time.Second)
	if err != nil || res.Exit != 0 || len(res.Failures) != 0 || !strings.HasPrefix(res.Summary, "ok: 1 passed") {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestBuildErrorIsAFailure(t *testing.T) {
	root := goProject(t)
	if err := os.WriteFile(filepath.Join(root, "good", "good.go"), []byte("package good\n\nfunc One() int { return \"x\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), root, []string{"go", "test", "./good"}, []string{"go test"}, 60*time.Second)
	if err != nil || res.Exit == 0 || len(res.Failures) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	f := res.Failures[0]
	if !strings.Contains(f.Message, "cannot use") || len(f.Frames) == 0 || f.Frames[0].File != "good/good.go" || f.Frames[0].Line != 3 {
		t.Fatalf("a compile error points at its file and line: %+v", f)
	}
}

func TestGenericRunnerOutputLosesNoiseAndVendorFrames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "Foo.php"), []byte("<?php\n"), 0o644)
	script := `printf '\033[32m....\033[0m\rPHPUnit 10\n\nThere was 1 failure:\n\n1) FooTest::testBar\nFailed asserting that 2 matches expected 3.\n\n/vendor/phpunit/phpunit/src/Framework/TestCase.php:100\n%s/src/Foo.php:12\n\nFAILURES!\nTests: 5, Assertions: 9, Failures: 1.\n' "` + root + `"; exit 1`
	res, err := Run(context.Background(), root, []string{"sh", "-c", script}, []string{"sh"}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit != 1 || res.Summary != "Tests: 5, Assertions: 9, Failures: 1." || len(res.Failures) != 1 {
		t.Fatalf("%+v", res)
	}
	f := res.Failures[0]
	if strings.Contains(f.Message, "vendor") || strings.Contains(f.Message, "\x1b") || !strings.Contains(f.Message, "Failed asserting that 2 matches expected 3.") {
		t.Fatalf("%q", f.Message)
	}
	if len(f.Frames) != 1 || f.Frames[0] != (Frame{"src/Foo.php", 12}) {
		t.Fatalf("%+v", f.Frames)
	}
}

func TestATimeoutStopsTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sleep")
	}
	res, err := Run(context.Background(), t.TempDir(), []string{"sleep", "5"}, []string{"sleep"}, 300*time.Millisecond)
	if err != nil || !res.TimedOut || res.Duration > 4*time.Second {
		t.Fatalf("%+v %v", res, err)
	}
}
