package index

import (
	"context"
	"fmt"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

func TestMentionedSkipsSelf(t *testing.T) {
	names := map[string]struct{}{"Execute": {}, "Run": {}}
	got := mentioned("func (s *Service) Execute() { s.Run() }", "Execute", names)
	if len(got) != 1 || got[0] != "Run" {
		t.Fatalf("%v", got)
	}
	if got := mentioned("svc.Execute()", "Run", names); len(got) != 1 || got[0] != "Execute" {
		t.Fatalf("%v", got)
	}
}

func TestLinkRefsFillsCalls(t *testing.T) {
	st, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	vec := []float32{1, 0, 0, 0}
	err = storetest.Save(ctx, st, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{
			FilePath: "svc.go", Type: "method", Content: "func (s *Service) Execute() { _ = App\\Models\\User }",
			Metadata: map[string]string{"name": "Execute"},
			Outbound: []string{"App\\Models\\User"},
		}, Vector: vec},
		{Chunk: parser.Chunk{
			FilePath: "run.go", Type: "function", Content: "func run() { svc.Execute() }",
			Metadata: map[string]string{"name": "run"},
		}, Vector: vec},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{store: st}
	if err := session.linkRefs(ctx, nil); err != nil {
		t.Fatal(err)
	}
	refs, err := st.GetReferences(ctx, "Execute")
	if err != nil || len(refs) != 1 || refs[0].FilePath != "run.go" {
		t.Fatalf("%+v %v", refs, err)
	}
	self, err := st.GetComponentAST(ctx, "Execute")
	if err != nil {
		t.Fatal(err)
	}
	if len(self.Outbound) != 1 || self.Outbound[0] != `App\Models\User` {
		t.Fatalf("outbound %+v", self.Outbound)
	}
	if err := session.linkRefs(ctx, nil); err != nil {
		t.Fatal(err)
	}
	self, err = st.GetComponentAST(ctx, "Execute")
	if err != nil || len(self.Outbound) != 1 {
		t.Fatalf("second %+v %v", self.Outbound, err)
	}
}

func TestLinkRefsIncrementalOnlyTouchesChangedFiles(t *testing.T) {
	st, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	vec := []float32{1, 0, 0, 0}
	commit := func(path, name, content string) {
		t.Helper()
		ch := parser.Chunk{FilePath: path, Type: "function", Content: content, Metadata: map[string]string{"name": name}}
		if err := st.CommitFile(ctx, path, content, []vectorstore.EmbeddedChunk{{Chunk: ch, Vector: vec}}); err != nil {
			t.Fatal(err)
		}
	}
	commit("a.go", "A", "func A() { B() }")
	commit("b.go", "B", "func B() {}")
	session := &Session{store: st}
	if err := session.linkRefs(ctx, nil); err != nil {
		t.Fatal(err)
	}
	commit("a.go", "A", "func A() { B(); A() }")
	if err := session.linkRefs(ctx, []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	a, err := st.GetComponentAST(ctx, "A")
	if err != nil || len(a.Outbound) != 1 || a.Outbound[0] != "B" {
		t.Fatalf("a outbound %+v %v", a, err)
	}
	commit("c.go", "C", "func C() {}")
	commit("b.go", "B", "func B() { C() }")
	if err := session.linkRefs(ctx, []string{"c.go"}); err != nil {
		t.Fatal(err)
	}
	b, err := st.GetComponentAST(ctx, "B")
	if err != nil || len(b.Outbound) != 1 || b.Outbound[0] != "C" {
		t.Fatalf("b outbound %+v %v", b, err)
	}
}

func TestMentionedFindsInjectedAndThrownClasses(t *testing.T) {
	names := map[string]struct{}{"RefundCalculator": {}, "Clock": {}, "FrozenException": {}, "int": {}}
	got := mentioned("/** @throws \\App\\FrozenException */\npublic function __construct(\n private readonly RefundCalculator $calc, ?Clock $c, int $n)", "x", names)
	if fmt.Sprint(got) != "[Clock FrozenException RefundCalculator]" {
		t.Fatalf("%v", got)
	}
}
