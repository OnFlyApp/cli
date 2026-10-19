package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/index"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/telemetry"
	"github.com/axitdev/onfly/internal/vectorstore"
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
	must(saveHashed(store, root, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "src/pay.go", Type: "function", Content: "func Charge() {\n...", Metadata: meta("Charge", 3, 14), Outbound: []string{"Lock"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "src/pay.go", Type: "function", Content: "func Lock() {}", Metadata: meta("Lock", 16, 16)}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "src/web.go", Type: "function", Content: "func Handle() { Charge() }", Metadata: meta("Handle", 1, 1), Outbound: []string{"Charge"}}, Vector: vec},
	}))
	cfg := config.Config{ProjectRoot: root, Rules: []string{filepath.Join(root, "rule.md")}, Server: config.Server{MaxTokens: 8192}}
	return cfg, store, root
}

// saveHashed stores fixture chunks under the real content hash of the file on
// disk, so the tools see it as up to date instead of syncing it.
func saveHashed(store vectorstore.Store, root string, chunks []vectorstore.EmbeddedChunk) error {
	by := map[string][]vectorstore.EmbeddedChunk{}
	var order []string
	for _, c := range chunks {
		p := c.Chunk.FilePath
		if _, ok := by[p]; !ok {
			order = append(order, p)
		}
		by[p] = append(by[p], c)
	}
	for _, p := range order {
		hash := "fixture"
		if raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			hash = index.ContentHash(raw)
		}
		if err := store.CommitFile(context.Background(), p, hash, by[p]); err != nil {
			return err
		}
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

func callText(t *testing.T, s *sdkmcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: %v %+v %v", name, err, res, res.Content[0].(*sdkmcp.TextContent).Text)
	}
	return res.Content[0].(*sdkmcp.TextContent).Text
}

func TestXrayFileSkeletonBlastRadiusAndRulesOnce(t *testing.T) {
	defer func(v int) { smallFile = v }(smallFile)
	smallFile = 0
	cfg, store, _ := macroFixture(t)
	cfg.Server.LogFile = filepath.Join(t.TempDir(), "server.log")
	session := openServer(t, cfg, store, fakeEmbedder{})
	first := callText(t, session, "onfly_xray", map[string]any{"target": "src/pay.go"})
	for _, want := range []string{`# onfly target="src/pay.go"`, "body omitted: lines 4-13", "func Handle() { Charge() } [src/web.go:1]", "Use the repository layer."} {
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
	if out := callText(t, session, "onfly_focus", map[string]any{"query": "Charge"}); !strings.Contains(out, "### Charge @") || !strings.Contains(out, "## symbols") {
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
	for _, want := range []string{"onfly_xray", "onfly_focus"} {
		if !got[want] {
			t.Errorf("%s missing: %v", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("the default surface is exactly two tools: %v", got)
	}
	for _, gone := range []string{"get_tests", "get_history", "search", "find", "graph", "get_component_ast", "get_file_outline", "get_project_rules"} {
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

type countingStore struct {
	vectorstore.Store
	single, batched int
}

func (c *countingStore) GetComponents(ctx context.Context, name string) ([]parser.Chunk, error) {
	c.single++
	return c.Store.GetComponents(ctx, name)
}

func (c *countingStore) GetReferences(ctx context.Context, name string) ([]parser.Chunk, error) {
	c.single++
	return c.Store.GetReferences(ctx, name)
}

func (c *countingStore) ComponentsByNames(ctx context.Context, names []string) (map[string][]parser.Chunk, error) {
	c.batched++
	return c.Store.ComponentsByNames(ctx, names)
}

func (c *countingStore) ReferencesByNames(ctx context.Context, names []string) (map[string][]parser.Chunk, error) {
	c.batched++
	return c.Store.ReferencesByNames(ctx, names)
}

func TestBlastRadiusUsesBatchedQueries(t *testing.T) {
	root := t.TempDir()
	var src strings.Builder
	src.WriteString("package big\n")
	store := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	var chunks []vectorstore.EmbeddedChunk
	for i := 0; i < 40; i++ {
		name := "F" + strconv.Itoa(i)
		src.WriteString("func " + name + "() {}\n")
		out := []string{"Ext" + strconv.Itoa(i), "Missing" + strconv.Itoa(i)}
		chunks = append(chunks,
			vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: "big.go", Type: "function", Content: "func " + name + "() { Ext" + strconv.Itoa(i) + "(); Missing" + strconv.Itoa(i) + "() }", Metadata: map[string]string{"name": name, "start_line": strconv.Itoa(i + 2), "end_line": strconv.Itoa(i + 2)}, Outbound: out}, Vector: vec},
			vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: "ext.go", Type: "function", Content: "func Ext" + strconv.Itoa(i) + "() {}", Metadata: map[string]string{"name": "Ext" + strconv.Itoa(i)}}, Vector: vec},
			vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: "use.go", Type: "function", Content: "func Use" + strconv.Itoa(i) + "() { " + name + "() }", Metadata: map[string]string{"name": "Use" + strconv.Itoa(i)}, Outbound: []string{name}}, Vector: vec},
		)
	}
	if err := os.WriteFile(filepath.Join(root, "big.go"), []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveHashed(store, root, chunks); err != nil {
		t.Fatal(err)
	}
	counted := &countingStore{Store: store}
	session := openServer(t, config.Config{ProjectRoot: root, Server: config.Server{MaxTokens: 8192}}, counted, fakeEmbedder{})
	out := callText(t, session, "onfly_xray", map[string]any{"target": "big.go", "depth": 2})
	if !strings.Contains(out, "func Ext0() {}") || !strings.Contains(out, "func Use0() { F0() }") {
		t.Fatalf("blast radius missing:\n%s", out)
	}
	if counted.single != 0 {
		t.Fatalf("%d per-name queries; the blast radius must use batched ones", counted.single)
	}
	if counted.batched > 1+2*2 {
		t.Fatalf("%d batched queries for depth 2, want at most 5", counted.batched)
	}
}

func TestRuleDedupIsPerServerAndInMemory(t *testing.T) {
	cfg, store, _ := macroFixture(t)
	for i := 0; i < 2; i++ {
		session := openServer(t, cfg, store, fakeEmbedder{})
		if out := callText(t, session, "onfly_xray", map[string]any{"target": "src/pay.go"}); !strings.Contains(out, "Use the repository layer.") {
			t.Fatalf("server %d: a new server starts with no rules sent:\n%s", i, out)
		}
	}
}

func TestBlastRadiusDoesNotMatchStdlibCallsToProjectNames(t *testing.T) {
	root := t.TempDir()
	src := "package pay\n\nimport (\n\t\"strings\"\n\n\t\"example.com/app/internal/tokens\"\n)\n\nfunc Charge(s string) int {\n\tparts := strings.Split(s, \",\")\n\tn, _ := tokens.Count(s)\n\treturn len(parts) + n\n}\n"
	if err := os.MkdirAll(filepath.Join(root, "internal", "pay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "pay", "pay.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	meta := func(name string) map[string]string {
		return map[string]string{"name": name, "start_line": "9", "end_line": "13"}
	}
	if err := saveHashed(store, root, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "internal/pay/pay.go", Type: "function", Content: "func Charge(s string) int {\n\tparts := strings.Split(s, \",\")\n\tn, _ := tokens.Count(s)\n\treturn len(parts) + n\n}", Metadata: meta("Charge"), Outbound: []string{"Split", "Count", "len"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "internal/eval/eval.go", Type: "function", Content: "func Split(cases []Case) {}", Metadata: map[string]string{"name": "Split"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "internal/tokens/tokens.go", Type: "function", Content: "func Count(s string) int { return 0 }", Metadata: map[string]string{"name": "Count"}}, Vector: vec},
	}); err != nil {
		t.Fatal(err)
	}
	session := openServer(t, config.Config{ProjectRoot: root, Server: config.Server{MaxTokens: 8192}}, store, fakeEmbedder{})
	out := callText(t, session, "onfly_xray", map[string]any{"target": "internal/pay/pay.go"})
	if strings.Contains(out, "func Split") {
		t.Fatalf("strings.Split resolved to the project's Split:\n%s", out)
	}
	if !strings.Contains(out, "func Count(s string) int") {
		t.Fatalf("tokens.Count must resolve through the import:\n%s", out)
	}
}

func TestXrayShowsASmallFileWhole(t *testing.T) {
	cfg, store, _ := macroFixture(t)
	cfg.Server.LogFile = filepath.Join(t.TempDir(), "server.log")
	session := openServer(t, cfg, store, fakeEmbedder{})
	got := callText(t, session, "onfly_xray", map[string]any{"target": "src/pay.go"})
	if strings.Contains(got, "body omitted") || !strings.Contains(got, "a := 1") {
		t.Fatalf("a file under %d lines keeps its bodies:\n%s", smallFile, got)
	}
}
