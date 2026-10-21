package query

import (
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func chunk(name, typ, content string) parser.Chunk {
	return parser.Chunk{FilePath: name + ".go", Type: typ, Content: content, Metadata: map[string]string{"name": name}}
}

func TestDemoteBoilerplateMovesTinyUnnamedTypesLast(t *testing.T) {
	ranked := []parser.Chunk{
		chunk("emptyIn", "struct", "type emptyIn struct{}"),
		chunk("Collapse", "function", "func Collapse() {\n\ta()\n\tb()\n\tc()\n\td()\n}"),
		chunk("SpanKind", "type", "type SpanKind int"),
		chunk("bodyOpen", "function", "func bodyOpen() {}"),
		chunk("Config", "struct", "type Config struct {\n\tA int\n\tB int\n\tC int\n\tD int\n\tE int\n}"),
	}
	got := demoteBoilerplate(ranked, []string{"skeleton", "body", "collapse", "spankind"})
	var names []string
	for _, c := range got {
		names = append(names, c.Metadata["name"])
	}
	want := []string{"Collapse", "SpanKind", "bodyOpen", "Config", "emptyIn"}
	if len(names) != len(want) {
		t.Fatalf("%v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("%v, want %v", names, want)
		}
	}
}

func TestFocusParamsWeighVectorsMore(t *testing.T) {
	p := FocusParams(Params{Semantic: 2, Keyword: 0.5, Word: 0.3, Field: 0.5, Alt: 1, RRFK: 20})
	if p.Semantic/p.Keyword < 3.5 || p.Word != 1 || p.Field != 1 || p.Alt != focusSemantic || p.RRFK != 20 {
		t.Fatalf("%+v", p)
	}
}

func TestBoostPathTokensLiftsTheNamedModule(t *testing.T) {
	at := func(file, name string) parser.Chunk {
		return parser.Chunk{FilePath: file, Type: "function", Content: "func x() {}", Metadata: map[string]string{"name": name}}
	}
	ranked := []parser.Chunk{
		at("internal/skeleton/skeleton.go", "Collapse"),
		at("internal/skeleton/skeleton.go", "bodyOpen"),
		at("internal/mcp/filetools.go", "readFileBody"),
		at("internal/skeleton/skeleton.go", "Signature"),
		at("internal/embedder/embedder.go", "postOnce"),
		at("internal/index/walk.go", "walk"),
	}
	got := boostPathTokens(ranked, nil, []string{"skeleton", "body", "collapse"}, nil)
	var names []string
	for _, c := range got[:4] {
		names = append(names, c.Metadata["name"])
	}
	want := []string{"Collapse", "bodyOpen", "Signature", "readFileBody"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("%v, want %v", names, want)
		}
	}
	if same := boostPathTokens(ranked[2:5], nil, []string{"skeleton"}, nil); same[0].Metadata["name"] != "readFileBody" {
		t.Fatalf("no path match must keep the order: %v", same)
	}
}

func TestBoostPathTokensSlotsInSymbolsNoListReturned(t *testing.T) {
	at := func(file, name string) parser.Chunk {
		return parser.Chunk{FilePath: file, Type: "function", Content: "func x() {}", Metadata: map[string]string{"name": name}}
	}
	var ranked []parser.Chunk
	for _, n := range []string{"Collapse", "bodyOpen", "readFileBody", "postOnce", "walk", "render"} {
		file := "internal/other/" + n + ".go"
		if n == "Collapse" || n == "bodyOpen" {
			file = "internal/skeleton/skeleton.go"
		}
		ranked = append(ranked, at(file, n))
	}
	extras := []parser.Chunk{at("internal/skeleton/skeleton.go", "Collapse"), at("internal/skeleton/skeleton.go", "Signature"), at("internal/skeleton/skeleton.go", "Span")}
	got := boostPathTokens(ranked, extras, []string{"skeleton", "body", "collapse"}, nil)
	var names []string
	for _, c := range got[:5] {
		names = append(names, c.Metadata["name"])
	}
	want := []string{"Collapse", "bodyOpen", "Signature", "Span", "readFileBody"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("%v, want %v", names, want)
		}
	}
}

func TestDemoteTestsKeepsImplementationFirstUnlessAskedAboutTests(t *testing.T) {
	ranked := []parser.Chunk{
		{FilePath: "internal/index/index_test.go", Metadata: map[string]string{"name": "TestIndex"}},
		{FilePath: "internal/index/index.go", Metadata: map[string]string{"name": "indexFile"}},
	}
	if got := demoteTests(ranked, []string{"embedder", "unreachable"}); got[0].FilePath != "internal/index/index.go" || got[1].FilePath != "internal/index/index_test.go" {
		t.Fatalf("%v", got)
	}
	if got := demoteTests(ranked, []string{"tests", "index"}); got[0].FilePath != "internal/index/index_test.go" {
		t.Fatalf("a query about tests keeps their order: %v", got)
	}
}

func TestDemoteBoilerplateMovesImportsAndNamespacesLast(t *testing.T) {
	ranked := []parser.Chunk{
		chunk("use Symfony\\Component\\Console\\Question;", "use", "use Symfony\\Component\\Console\\Question;"),
		chunk("Illuminate\\Notifications", "namespace", "namespace Illuminate\\Notifications;"),
		chunk("verify", "method", "public function verify($x)\n{\n    a();\n    b();\n    c();\n}"),
	}
	got := demoteBoilerplate(ranked, []string{"verify"})
	if got[0].Metadata["name"] != "verify" {
		t.Fatalf("code leads imports: %v", got)
	}
}

func TestPathWeightCountsCamelCaseWordsOfTheWholePath(t *testing.T) {
	terms := []string{"invoice", "period", "close", "handler"}
	both := pathWeight("src/InvoicePeriod/Command/ClosePeriodHandler.php", terms, nil)
	part := pathWeight("src/RewardPeriod/Command/CloseRewardPeriodHandler.php", terms, nil)
	if both <= part || both < 0.9 {
		t.Fatalf("a path naming all four words must beat one naming three: %v %v", both, part)
	}
}

func TestCoveringFilesFindsThePathThatNamesTheQuestion(t *testing.T) {
	files := []string{
		"src/InvoicePeriod/Command/ClosePeriodHandler.php",
		"tests/Unit/InvoicePeriod/Command/ClosePeriodHandlerTest.php",
		"src/RewardPeriod/Command/CloseRewardPeriodHandler.php",
		"src/Other/Thing.php",
	}
	got := coveringFiles(files, []string{"handler", "closes", "invoice", "period"}, true)
	if len(got) == 0 || got[0] != files[0] {
		t.Fatalf("%v", got)
	}
	for _, f := range got {
		if f == files[1] {
			t.Fatal("tests are not picked")
		}
	}
}

func TestSearchConfigOverridesTheFocusWeightsAndThePool(t *testing.T) {
	s := &Searcher{}
	if p := s.focusParams(); p.Semantic != focusSemantic || p.Keyword != focusLexical {
		t.Fatalf("the defaults: %+v", p)
	}
	sem, kw := 3.0, 0.25
	s = &Searcher{FocusSemantic: &sem, FocusKeyword: &kw, Pool: 30}
	if p := s.focusParams(); p.Semantic != 3 || p.Keyword != 0.25 {
		t.Fatalf("%+v", p)
	}
	if s.candidatePool(5) != 30 || s.candidatePool(40) != 40 || (&Searcher{}).candidatePool(5) != keywordPool {
		t.Fatal("the pool is the larger of the limit and search.candidate_pool")
	}
}
