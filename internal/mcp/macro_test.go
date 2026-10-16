package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/telemetry"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const payFile = `package pay

func Charge() {
	a := 1
	b := 2
	c := 3
	d := 4
	e := 5
	f := 6
	g := 7
	h := 8
	_ = a + b + c + d + e + f + g + h
	Lock()
}

func Lock() {}
`

func macroFixture(t *testing.T) (config.Config, vectorstore.Store, string) {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "src"), 0o755))
	must(os.WriteFile(filepath.Join(root, "src", "pay.go"), []byte(payFile), 0o644))
	must(os.WriteFile(filepath.Join(root, "rule.md"), []byte("---\nname: Money\nscope: [\"src/**\"]\n---\nUse the repository layer.\n"), 0o644))
	store := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	meta := func(name string, start, end int) map[string]string {
		return map[string]string{"name": name, "start_line": itoa(start), "end_line": itoa(end)}
	}
	must(storetest.Save(context.Background(), store, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "src/pay.go", Type: "function", Content: "func Charge() {\n...", Metadata: meta("Charge", 3, 14), Outbound: []string{"Lock"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "src/pay.go", Type: "function", Content: "func Lock() {}", Metadata: meta("Lock", 16, 16)}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "src/web.go", Type: "function", Content: "func Handle() { Charge() }", Metadata: meta("Handle", 1, 1), Outbound: []string{"Charge"}}, Vector: vec},
	}))
	cfg := config.Config{ProjectRoot: root, Rules: []string{filepath.Join(root, "rule.md")}, Server: config.Server{MaxTokens: 8192}}
	return cfg, store, root
}

func itoa(n int) string { return strconv.Itoa(n) }

func callText(t *testing.T, s *sdkmcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: %v %+v", name, err, res)
	}
	return res.Content[0].(*sdkmcp.TextContent).Text
}

func TestXrayFileSkeletonBlastRadiusAndRulesOnce(t *testing.T) {
	cfg, store, _ := macroFixture(t)
	cfg.Server.LogFile = filepath.Join(t.TempDir(), "server.log")
	session := openServer(t, cfg, store, fakeEmbedder{})
	first := callText(t, session, "onfly_xray", map[string]any{"target": "src/pay.go"})
	for _, want := range []string{`<onfly_context target="src/pay.go"`, "body omitted: lines 4-13", "func Handle() { Charge() } [src/web.go:1]", "Use the repository layer."} {
		if !strings.Contains(first, want) {
			t.Fatalf("missing %q in\n%s", want, first)
		}
	}
	if strings.Contains(first, "a := 1") {
		t.Fatalf("body must be collapsed:\n%s", first)
	}
	second := callText(t, session, "onfly_xray", map[string]any{"target": "src/pay.go", "focus_symbol": "Charge"})
	if strings.Contains(second, "Use the repository layer.") {
		t.Fatalf("rule must be sent once per session:\n%s", second)
	}
	if !strings.Contains(second, "a := 1") {
		t.Fatalf("focus_symbol must keep the body:\n%s", second)
	}
	if tool := callText(t, session, "onfly_xray", map[string]any{"target": "Charge"}); !strings.Contains(tool, `target="src/pay.go"`) {
		t.Fatalf("a symbol resolves to its file:\n%s", tool)
	}
}

func TestXrayUnknownTargetIsAnError(t *testing.T) {
	cfg, store, _ := macroFixture(t)
	session := openServer(t, cfg, store, fakeEmbedder{})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "onfly_xray", Arguments: map[string]any{"target": "nope.go"}})
	if err != nil || !res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestMacroToolsRecordTelemetry(t *testing.T) {
	cfg, store, _ := macroFixture(t)
	cfg.Server.LogFile = filepath.Join(t.TempDir(), "server.log")
	session := openServer(t, cfg, store, fixedEmbedder{v: []float32{1, 0, 0, 1}})
	callText(t, session, "onfly_xray", map[string]any{"target": "src/pay.go"})
	if out := callText(t, session, "onfly_focus", map[string]any{"query": "Charge"}); !strings.Contains(out, `name="Charge"`) || !strings.Contains(out, "<related>") {
		t.Fatalf("focus bundle:\n%s", out)
	}
	tel, err := telemetry.Open(cfg.Server.TelemetryPath())
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	s, err := tel.Summary(context.Background())
	if err != nil || s.Requests != 2 || s.RawBytes == 0 || s.Rules != 1 {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestDefaultSurfaceIsTheMacroTools(t *testing.T) {
	session := openSessionAs(t, config.Config{})
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"onfly_xray", "onfly_focus", "get_tests", "get_history"} {
		if !got[want] {
			t.Errorf("%s missing: %v", want, got)
		}
	}
	for _, gone := range []string{"search", "find", "graph", "get_component_ast", "get_file_outline", "get_project_rules"} {
		if got[gone] {
			t.Errorf("%s must be opt-in", gone)
		}
	}
}

func openSessionAs(t *testing.T, cfg config.Config) *sdkmcp.ClientSession {
	t.Helper()
	srv, cleanup, err := NewServer(cfg, seedUsers(t), fakeEmbedder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	st, ct := sdkmcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}
