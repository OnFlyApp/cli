package mcp

import (
	"context"
	"testing"

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
