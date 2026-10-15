package query

import (
	"context"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

func chunkIn(file, name, content string) parser.Chunk {
	return parser.Chunk{FilePath: file, Type: "function", Content: content, Metadata: map[string]string{"name": name}}
}

func TestGroupByFileKeepsOneChunkPerFile(t *testing.T) {
	s := searcherWith(t,
		chunkIn("a.go", "alpha", "func alpha() { widget }"),
		chunkIn("a.go", "alphaTwo", "func alphaTwo() { widget }"),
		chunkIn("b.go", "beta", "func beta() { widget }"),
	)
	ctx := context.Background()
	all, err := s.Search(ctx, "widget", 10, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("chunk mode returns every chunk: %+v %v", all, err)
	}
	got, err := s.Run(ctx, Request{Query: "widget", Limit: 10, GroupBy: "file"})
	if err != nil || len(got) != 2 {
		t.Fatalf("file mode returns one chunk per file: %+v %v", got, err)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.FilePath] {
			t.Fatalf("duplicate file %s", c.FilePath)
		}
		seen[c.FilePath] = true
	}
	s.GroupBy = "file"
	got, _ = s.Search(ctx, "widget", 10, "")
	if len(got) != 2 {
		t.Fatalf("the searcher default applies: %+v", got)
	}
	got, _ = s.Run(ctx, Request{Query: "widget", Limit: 10, GroupBy: "chunk"})
	if len(got) != 3 {
		t.Fatalf("the request overrides the default: %+v", got)
	}
	if _, err := s.Run(ctx, Request{Query: "widget", GroupBy: "bogus"}); err == nil {
		t.Fatal("unknown group_by must fail")
	}
}

func TestDedupeDropsRepeatedText(t *testing.T) {
	s := searcherWith(t,
		chunkIn("a.go", "same", "func same() { gadget }"),
		chunkIn("vendor/a.go", "same", "func same() { gadget }"),
		chunkIn("b.go", "diff", "func diff() { gadget }"),
	)
	got, _ := s.Search(context.Background(), "gadget", 10, "")
	if len(got) != 3 {
		t.Fatalf("off by default: %+v", got)
	}
	s.Dedupe = true
	got, _ = s.Search(context.Background(), "gadget", 10, "")
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestParamsDefaultsAndOverrides(t *testing.T) {
	s := &Searcher{}
	p := s.Params()
	if p.Semantic != defaultSemanticWeight || p.Keyword != defaultKeywordWeight || p.Word != defaultWordWeight || p.Field != defaultFieldWeight || p.RRFK != defaultRRFK {
		t.Fatalf("%+v", p)
	}
	zero, two := 0.0, 2.0
	s = &Searcher{WordWeight: &zero, FieldWeight: &two, RRFK: 20, SemanticWeight: 3}
	p = s.Params()
	if p.Word != 0 || p.Field != 2 || p.RRFK != 20 || p.Semantic != 3 {
		t.Fatalf("zero must turn a list off: %+v", p)
	}
}

func TestFuseWithParamsChangesRanking(t *testing.T) {
	s := searcherWith(t,
		chunkIn("a.go", "alpha", "func alpha() { widget }"),
		chunkIn("b.go", "widgetMaker", "func widgetMaker() {}"),
	)
	g, err := s.Gather(context.Background(), Request{Query: "widget maker", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	p := s.Params()
	p.Field = 0
	base := s.Fuse(g, p)
	p.Field = 5
	boosted := s.Fuse(g, p)
	if len(boosted) == 0 || boosted[0].Metadata["name"] != "widgetMaker" {
		t.Fatalf("a high field weight must lead with the named chunk: base %+v boosted %+v", base, boosted)
	}
}

func TestGraphLinkedOrdersByLinks(t *testing.T) {
	a := parser.Chunk{FilePath: "a.go", Metadata: map[string]string{"name": "A"}, Outbound: []string{"B", "C"}}
	b := parser.Chunk{FilePath: "b.go", Metadata: map[string]string{"name": "B"}}
	c := parser.Chunk{FilePath: "c.go", Metadata: map[string]string{"name": "C"}, Outbound: []string{"B"}}
	d := parser.Chunk{FilePath: "d.go", Metadata: map[string]string{"name": "D"}}
	got := graphLinked([]parser.Chunk{d, a, b, c})
	if len(got) != 3 || got[0].Metadata["name"] != "A" && got[0].Metadata["name"] != "B" {
		t.Fatalf("%+v", got)
	}
	for _, x := range got {
		if x.Metadata["name"] == "D" {
			t.Fatal("an unlinked hit must not join the graph list")
		}
	}
}

func TestGraphWeightLiftsLinkedHit(t *testing.T) {
	caller := parser.Chunk{FilePath: "a.go", Type: "function", Content: "func Handle() { widget; run() }", Metadata: map[string]string{"name": "Handle"}, Outbound: []string{"run"}}
	callee := parser.Chunk{FilePath: "z.go", Type: "function", Content: "func run() { widget }", Metadata: map[string]string{"name": "run"}}
	lone := parser.Chunk{FilePath: "b.go", Type: "function", Content: "func lone() { widget widget widget }", Metadata: map[string]string{"name": "lone"}}
	s := searcherWith(t, lone, callee, caller)
	zero := 0.0
	s.FieldWeight, s.WordWeight = &zero, &zero
	s.GraphWeight = 0
	base, _ := s.Search(context.Background(), "widget", 5, "")
	s.GraphWeight = 50
	got, _ := s.Search(context.Background(), "widget", 5, "")
	if len(got) != 3 || got[2].Metadata["name"] != "lone" {
		t.Fatalf("linked hits must outrank the unlinked one: base=%v got=%v", names(base), names(got))
	}
}

func names(cs []parser.Chunk) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Metadata["name"])
	}
	return out
}

func TestUsedChunksOrdersByCount(t *testing.T) {
	a := parser.Chunk{FilePath: "a.go", Metadata: map[string]string{"name": "A"}}
	b := parser.Chunk{FilePath: "b.go", Metadata: map[string]string{"name": "B"}}
	got := usedChunks(map[string]int{"b.go\x00B": 3, "a.go\x00A": 1}, []parser.Chunk{a, b, {FilePath: "c.go"}})
	if len(got) != 2 || got[0].FilePath != "b.go" {
		t.Fatalf("%+v", got)
	}
	if usedChunks(nil, []parser.Chunk{a}) != nil {
		t.Fatal("no counts, no list")
	}
}

func TestSummaryKeywordListFindsChunkByItsSummary(t *testing.T) {
	target := parser.Chunk{FilePath: "retry.go", Type: "function", Content: "func backoffLoop() { sleep }",
		Metadata: map[string]string{"name": "backoffLoop", "summary": "Pauses between attempts to contact the server."}}
	other := chunkIn("x.go", "other", "func other() {}")
	s := searcherWith(t, other, target)
	got, _ := s.Search(context.Background(), "pauses between attempts", 5, "")
	for _, c := range got {
		if c.FilePath == "retry.go" && got[0].FilePath == "retry.go" {
			t.Skip("found without the summary index")
		}
	}
	s.SummaryKeyword = true
	got, err := s.Search(context.Background(), "pauses between attempts", 5, "")
	if err != nil || len(got) == 0 || got[0].FilePath != "retry.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSummaryVectorListUsesItsSlot(t *testing.T) {
	// Both chunks have the same code vector (searcherWith); only the summary
	// slot tells them apart, and it points at the second one.
	a := chunkIn("a.go", "alpha", "func alpha() { widget }")
	b := chunkIn("b.go", "beta", "func beta() { widget }")
	s := searcherWith(t, a, b)
	if err := s.Store.InitExtra(context.Background(), "summary", 4); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.CommitFile(context.Background(), "b.go", "h", []vectorstore.EmbeddedChunk{
		{Chunk: b, Vector: []float32{1, 0, 0, 0}, Extra: map[string][]float32{"summary": {1, 0, 0, 0}}},
	}); err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	s.FieldWeight, s.WordWeight = &zero, &zero
	base, _ := s.Search(context.Background(), "widget", 5, "")
	s.SummaryVector = true
	got, err := s.Search(context.Background(), "widget", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "b.go" {
		t.Fatalf("the chunk with a summary vector must lead: base=%v got=%v err=%v", names(base), names(got), err)
	}
}
