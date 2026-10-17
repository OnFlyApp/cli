package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func runFixture(t *testing.T, allow ...string) *tools {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs the go tool")
	}
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/demo\n\ngo 1.21\n")
	write("calc/calc.go", "package calc\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n")
	write("calc/calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Errorf(\"Add(2, 3) = %d, want 5\", got)\n\t}\n}\n")
	m := func(name, s, e string) map[string]string {
		return map[string]string{"name": name, "start_line": s, "end_line": e}
	}
	v := []float32{1, 0, 0, 1}
	store := openTestStore(t)
	if err := saveHashed(store, root, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "calc/calc.go", Type: "function", Content: "func Add(a, b int) int {\n\treturn a - b\n}", Metadata: m("Add", "3", "5")}, Vector: v},
		{Chunk: parser.Chunk{FilePath: "calc/calc_test.go", Type: "function", Content: "func TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Errorf(\"Add(2, 3) = %d, want 5\", got)\n\t}\n}", Metadata: m("TestAdd", "5", "9")}, Vector: v},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ProjectRoot: root, Run: config.Run{Commands: allow}}
	return &tools{cfg: cfg, root: root, store: store, maxTokens: 8192}
}

func TestRunReturnsTheFailureAndTheFunctionsItPointsAt(t *testing.T) {
	h := runFixture(t, "go test")
	res, _, err := h.runCommand(context.Background(), nil, runIn{Command: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(res)
	for _, want := range []string{`exit="1"`, "<failure name=\"TestAdd\" package=\"calc\">", "Add(2, 3) = -1, want 5", `<code at="calc/calc_test.go:5">`, "func TestAdd"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "=== RUN") || strings.Contains(out, `"Action"`) {
		t.Fatalf("the raw event stream must not reach the model:\n%s", out)
	}
}

func TestRunRefusesWhatIsNotAllowed(t *testing.T) {
	h := runFixture(t, "go test")
	for _, cmd := range []string{"go build ./...", "rm -rf /", "go test ./... && echo hi", "go test | cat"} {
		if _, _, err := h.runCommand(context.Background(), nil, runIn{Command: cmd}); err == nil {
			t.Errorf("%q must be refused", cmd)
		}
	}
	none := runFixture(t)
	if _, _, err := none.runCommand(context.Background(), nil, runIn{Command: "go test ./..."}); err == nil || !strings.Contains(err.Error(), "run.commands") {
		t.Fatalf("with no allowlist nothing runs: %v", err)
	}
}

func TestRunToolIsRegisteredOnlyWhenSwitchedOnAndIsNotReadOnly(t *testing.T) {
	h := runFixture(t, "go test")
	cfg := h.cfg
	cfg.Server.MaxTokens = 8192
	list := func(c config.Config) map[string]*sdkmcp.Tool {
		session := openServer(t, c, h.store, fakeEmbedder{})
		res, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]*sdkmcp.Tool{}
		for _, tool := range res.Tools {
			m[tool.Name] = tool
		}
		return m
	}
	if _, ok := list(cfg)["onfly_run"]; ok {
		t.Fatal("off by default")
	}
	cfg.Server.Tools = []string{"onfly_run"}
	tool, ok := list(cfg)["onfly_run"]
	if !ok || tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
		t.Fatalf("a tool that starts processes is not read-only: %+v", tool)
	}
}
