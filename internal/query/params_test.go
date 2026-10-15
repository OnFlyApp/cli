package query

import (
	"context"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func chunkIn(file, name, content string) parser.Chunk {
	return parser.Chunk{FilePath: file, Type: "function", Content: content, Metadata: map[string]string{"name": name}}
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

func TestTypeWeightsDemoteSmallDeclarations(t *testing.T) {
	use := parser.Chunk{FilePath: "a.php", Type: "trait_use", Content: "use PasswordRules; widget", Metadata: map[string]string{"name": "PasswordRules"}}
	def := parser.Chunk{FilePath: "b.php", Type: "trait", Content: "trait PasswordRules { widget }", Metadata: map[string]string{"name": "PasswordRules"}}
	s := searcherWith(t, use, def)
	zero := 0.0
	s.FieldWeight, s.WordWeight = &zero, &zero
	s.TypeWeights = map[string]float64{"trait_use": 0}
	got, _ := s.Search(context.Background(), "widget", 5, "")
	if len(got) != 2 || got[0].Type != "trait" || got[1].Type != "trait_use" {
		t.Fatalf("a weighted-down type must sort below the definition, and stay in the results: %+v", got)
	}
}
