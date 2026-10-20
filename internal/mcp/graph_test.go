package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/ignore"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

func openTestStore(t *testing.T) vectorstore.Store {
	t.Helper()
	st, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestImpactWalksCallersAndStopsOnCycles(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	chunks := []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: "func A() { B() }", Metadata: map[string]string{"name": "A"}, Outbound: []string{"B"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "b.go", Type: "function", Content: "func B() { A(); C() }", Metadata: map[string]string{"name": "B"}, Outbound: []string{"A", "C"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "c.go", Type: "function", Content: "func C() {}", Metadata: map[string]string{"name": "C"}}, Vector: vec},
	}
	if err := storetest.Save(ctx, st, chunks); err != nil {
		t.Fatal(err)
	}
	h := &tools{store: st}
	got, err := h.impact(ctx, impactIn{Name: "C", Depth: 4})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, c := range got {
		names = append(names, c.Metadata["name"])
	}
	if len(got) != 2 || names[0] != "B" || names[1] != "A" {
		t.Fatalf("%v", names)
	}
}

func TestCalleesReturnsDefinedNamesOnly(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	if err := storetest.Save(ctx, st, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: "func A() { B(); fmt.Println() }", Metadata: map[string]string{"name": "A"}, Outbound: []string{"B", "Println"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "b.go", Type: "function", Content: "func B() {}", Metadata: map[string]string{"name": "B"}}, Vector: vec},
	}); err != nil {
		t.Fatal(err)
	}
	h := &tools{store: st}
	got, err := h.callees(ctx, calleesIn{Name: "A"})
	if err != nil || len(got) != 1 || got[0].Metadata["name"] != "B" {
		t.Fatalf("%+v %v", got, err)
	}
}

func ambiguousStore(t *testing.T) vectorstore.Store {
	t.Helper()
	st := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	fn := func(file, name, content string, out ...string) vectorstore.EmbeddedChunk {
		return vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: file, Type: "function", Content: content,
			Metadata: map[string]string{"name": name}, Outbound: out}, Vector: vec}
	}
	if err := storetest.Save(context.Background(), st, []vectorstore.EmbeddedChunk{
		fn("pkg/y/run.go", "Run", "func Run() {}"),
		fn("pkg/x/run.go", "Run", "func Run() {}"),
		fn("pkg/x/use.go", "UseX", "func UseX() { Run() }", "Run"),
		fn("pkg/y/use.go", "UseY", "func UseY() { Run() }", "Run"),
		fn("cmd/top.go", "Top", "func Top() { UseX() }", "UseX"),
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCalleesPicksNearestDefinition(t *testing.T) {
	h := &tools{store: ambiguousStore(t)}
	got, err := h.callees(context.Background(), calleesIn{Name: "UseX"})
	if err != nil || len(got) != 1 || got[0].FilePath != "pkg/x/run.go" {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = h.callees(context.Background(), calleesIn{Name: "UseY"})
	if err != nil || len(got) != 1 || got[0].FilePath != "pkg/y/run.go" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := h.callees(context.Background(), calleesIn{Name: "Run", FilePath: "nope.go"}); err == nil {
		t.Fatal("an unmatched file_path must fail")
	}
}

func TestImpactScopedToOneDefinition(t *testing.T) {
	ctx := context.Background()
	h := &tools{store: ambiguousStore(t)}
	all, err := h.impact(ctx, impactIn{Name: "Run"})
	if err != nil || len(all) != 3 {
		t.Fatalf("unscoped must keep every same-name caller: %d %v", len(all), err)
	}
	got, err := h.impact(ctx, impactIn{Name: "Run", FilePath: "pkg/x/run.go", Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, c := range got {
		names[c.Metadata["name"]] = true
	}
	if len(got) != 2 || !names["UseX"] || !names["Top"] {
		t.Fatalf("%v", names)
	}
}

func TestReceiverMatchesShortAndQualifiedNames(t *testing.T) {
	cases := []struct {
		have, want string
		ok         bool
	}{
		{`App\Pipeline\ModelCaller`, "ModelCaller", true},
		{`App\Pipeline\ModelCaller`, `Pipeline\ModelCaller`, true},
		{"pkg.Service", "Service", true},
		{"Service", "Service", true},
		{`App\Pipeline\XModelCaller`, "ModelCaller", false},
		{`App\Pipeline\ModelCaller`, "Caller", false},
		{"", "Service", false},
	}
	for _, c := range cases {
		if got := receiverMatches(c.have, c.want); got != c.ok {
			t.Errorf("receiverMatches(%q, %q) = %v", c.have, c.want, got)
		}
	}
}

func TestGraphReturnsSignaturesUnlessAskedForBodies(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	body := "func A() {\n\tB()\n\tmore()\n}"
	if err := storetest.Save(ctx, st, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: body, Metadata: map[string]string{"name": "A"}, Outbound: []string{"B"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "b.go", Type: "function", Content: "func B() {}", Metadata: map[string]string{"name": "B"}}, Vector: vec},
	}); err != nil {
		t.Fatal(err)
	}
	h := &tools{store: st, maxTokens: 8192}
	_, out, err := h.graph(ctx, nil, graphIn{Name: "B", Direction: "callers"})
	if err != nil || len(out.Hits) != 1 || !out.Hits[0].Brief || out.Hits[0].Lines != "func A() {" {
		t.Fatalf("callers default to signatures: %+v %v", out.Hits, err)
	}
	_, out, err = h.graph(ctx, nil, graphIn{Name: "B", Direction: "callers", Detail: "full"})
	if err != nil || len(out.Hits) != 1 || out.Hits[0].Brief || out.Hits[0].Lines != body {
		t.Fatalf("detail full keeps the body: %+v %v", out.Hits, err)
	}
	if _, _, err := h.graph(ctx, nil, graphIn{Name: "B", Detail: "huge"}); err == nil {
		t.Fatal("an unknown detail is a tool error")
	}
}

func TestRelatedListsCallersAndTests(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	top := parser.Chunk{FilePath: "c.go", Type: "function", Content: "func C() {}", Metadata: map[string]string{"name": "C"}}
	if err := storetest.Save(ctx, st, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: "func A() { C() }", Metadata: map[string]string{"name": "A"}, Outbound: []string{"C"}}, Vector: vec},
		{Chunk: top, Vector: vec},
	}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "c_test.go"), []byte("func TestC(t *testing.T) {\n\tC()\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	filter, err := ignore.LoadFilter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &tools{store: st, root: root, filter: filter}
	callers, tests := h.related(ctx, top)
	if len(callers) != 1 || callers[0].Symbol != "A" || !callers[0].Brief {
		t.Fatalf("callers %+v", callers)
	}
	if len(tests) != 1 || tests[0].File != "c_test.go" || tests[0].Line != 2 {
		t.Fatalf("tests %+v", tests)
	}
}

func TestAttributedWithLetsTheLanguageOverrideCloseness(t *testing.T) {
	root := t.TempDir()
	rel := "src/Queue/RateLimited.php"
	if err := os.MkdirAll(filepath.Join(root, "src", "Queue"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "<?php\nnamespace App\\Queue;\nuse App\\Cache\\RateLimiter;\nclass RateLimited {\n    protected $limiter;\n    function __construct() { $this->limiter = make(RateLimiter::class); }\n}\n"
	if err := os.WriteFile(filepath.Join(root, rel), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	caller := parser.Chunk{FilePath: rel, Content: "return $this->limiter->tooManyAttempts($k, 1);", Metadata: map[string]string{"name": "handleJob", "namespace": "App\\Queue"}}
	cache := parser.Chunk{FilePath: "src/Cache/RateLimiter.php", Metadata: map[string]string{"name": "tooManyAttempts", "receiver": "App\\Cache\\RateLimiter", "namespace": "App\\Cache"}}
	next := parser.Chunk{FilePath: "src/Queue/RateLimitedWithRedis.php", Metadata: map[string]string{"name": "tooManyAttempts", "receiver": "App\\Queue\\RateLimitedWithRedis", "namespace": "App\\Queue"}}
	srcs := &sources{root: root, text: map[string]string{}}
	defs := []parser.Chunk{cache, next}
	if got := attributedWith(srcs, defs, definition{"tooManyAttempts", cache.FilePath}, []parser.Chunk{caller}); len(got) != 1 {
		t.Fatalf("the property is a Cache\\RateLimiter, so the call belongs to it though the other definition is nearer: %v", got)
	}
	if got := attributedWith(srcs, defs, definition{"tooManyAttempts", next.FilePath}, []parser.Chunk{caller}); len(got) != 1 {
		t.Fatalf("closeness alone would also accept it here, the resolver step rejects it later: %v", got)
	}
}
