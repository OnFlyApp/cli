package vectorstore

import (
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func TestParseVecLiteralRoundTrip(t *testing.T) {
	in := []float32{1, -0.5, 3.25, 0}
	got, err := parseVecLiteral(vecLiteral(in))
	if err != nil || len(got) != len(in) {
		t.Fatalf("%v %v", got, err)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("%v != %v", got, in)
		}
	}
	if _, err := parseVecLiteral("nope"); err == nil {
		t.Fatal("bad literal must fail")
	}
}

func TestAssembleComponentUnionsOutbound(t *testing.T) {
	mk := func(part, content string, out ...string) parser.Chunk {
		return parser.Chunk{FilePath: "a.go", Content: content, Outbound: out,
			Metadata: map[string]string{"name": "F", "part": part}}
	}
	got, err := assembleComponent([]parser.Chunk{mk("1/3", "a", "x"), mk("2/3", "b", "z", "y"), mk("3/3", "c", "y", "w")})
	if err != nil {
		t.Fatal(err)
	}
	if want := "w,x,y,z"; strings.Join(got.Outbound, ",") != want {
		t.Fatalf("%v", got.Outbound)
	}
}
