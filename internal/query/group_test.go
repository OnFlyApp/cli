package query

import (
	"context"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
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
