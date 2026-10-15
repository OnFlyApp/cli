package mcp

import (
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/tokens"
)

func TestSearchDetailAutoShrinksTail(t *testing.T) {
	chunks := make([]parser.Chunk, 10)
	for i := range chunks {
		chunks[i] = parser.Chunk{FilePath: "a.go", Content: "func F() {\n\tbody()\n\tmore()\n}", Metadata: map[string]string{"name": "F"}}
	}
	full, auto, brief := shapeHits(chunks, "full"), shapeHits(chunks, "auto"), shapeHits(chunks, "brief")
	for i := range chunks {
		if full[i].Brief || full[i].Lines != chunks[i].Content {
			t.Fatalf("full keeps bodies: %+v", full[i])
		}
	}
	if auto[2].Brief || !auto[3].Brief || auto[3].Lines != "func F() {" {
		t.Fatalf("auto keeps 3 bodies then signatures: %+v %+v", auto[2], auto[3])
	}
	if !brief[0].Brief {
		t.Fatalf("brief shrinks every hit: %+v", brief[0])
	}
	if shapeHits(chunks, "")[5].Brief != true {
		t.Fatal("an empty detail means auto")
	}
	nf, _ := tokens.Count(string(mustJSON(full)))
	na, _ := tokens.Count(string(mustJSON(auto)))
	if na >= nf {
		t.Fatalf("auto must cost fewer tokens than full: %d >= %d", na, nf)
	}
}
