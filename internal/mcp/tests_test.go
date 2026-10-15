package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsTestFile(t *testing.T) {
	for _, p := range []string{"a/b_test.go", "x/test_y.py", "x/y_test.py", "web/a.test.ts", "web/a.spec.jsx", "src/FooTest.php",
		"src/FooTests.cs", "Sources/FooTests.swift", "spec/models/user_spec.rb", "pkg/tests/a.go", "app/__tests__/b.js", "src/parse_test.rs"} {
		if !isTestFile(p) {
			t.Errorf("%s should be a test file", p)
		}
	}
	for _, p := range []string{"a/b.go", "contest/a.go", "latest.py", "src/protest.ts", "main.go"} {
		if isTestFile(p) {
			t.Errorf("%s should not be a test file", p)
		}
	}
}

func TestFindTestsReportsEnclosingTest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a_test.go"), "package a\n\nfunc TestRetry(t *testing.T) {\n\tgot := Retry(3)\n\t_ = got\n}\n")
	writeFile(t, filepath.Join(dir, "a.go"), "package a\nfunc Retry(n int) int { return n }\n")
	got, trunc, err := findTests(dir, nil, "Retry", "", 20)
	if err != nil || trunc || len(got) != 1 {
		t.Fatalf("%+v %v %v", got, trunc, err)
	}
	if got[0].File != "a_test.go" || got[0].Line != 4 || got[0].Enclosing != "func TestRetry(t *testing.T) {" {
		t.Fatalf("%+v", got[0])
	}
}

func TestFindTestsOtherLanguages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "web/a.test.ts"), "describe('x', () => {\n  it('retries', () => { retry(3) })\n})\n")
	writeFile(t, filepath.Join(dir, "tests/test_a.py"), "def test_retry():\n    assert retry(3)\n")
	got, _, err := findTests(dir, nil, "retry", "", 20)
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	py := map[string]string{}
	for _, h := range got {
		py[h.File] = h.Enclosing
	}
	if py["tests/test_a.py"] != "def test_retry():" || !strings.HasPrefix(py["web/a.test.ts"], "it(") {
		t.Fatalf("%v", py)
	}
}

func TestFindTestsCapsHits(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a_test.go"), strings.Repeat("x := Run()\n", 50))
	got, trunc, _ := findTests(dir, nil, "Run", "", 20)
	if len(got) != 20 || !trunc {
		t.Fatalf("%d %v", len(got), trunc)
	}
}

func TestFindTestsNoMatchIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a_test.go"), "package a\n")
	got, trunc, err := findTests(dir, nil, "Nope", "", 20)
	if err != nil || trunc || got == nil || len(got) != 0 {
		t.Fatalf("%+v %v %v", got, trunc, err)
	}
}

func TestFindTestsPathGlob(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a/x_test.go"), "Foo()\n")
	writeFile(t, filepath.Join(dir, "b/x_test.go"), "Foo()\n")
	got, _, _ := findTests(dir, nil, "Foo", "a/**", 20)
	if len(got) != 1 || got[0].File != "a/x_test.go" {
		t.Fatalf("%+v", got)
	}
}
