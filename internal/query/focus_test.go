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

func TestFocusParamsWeighLexicalListsMore(t *testing.T) {
	p := FocusParams(Params{Semantic: 2, Keyword: 0.5, Word: 0.3, Field: 0.5, Alt: 1, RRFK: 20})
	if p.Keyword/p.Semantic < 1.8 || p.Word != 1 || p.Field != 1 || p.Alt != focusSemantic || p.RRFK != 20 {
		t.Fatalf("%+v", p)
	}
}
