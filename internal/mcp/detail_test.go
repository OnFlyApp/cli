package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
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

func TestInstructionsAndToolDescriptions(t *testing.T) {
	store := seedUsers(t)
	session := openServer(t, config.Config{}, store, fakeEmbedder{})
	if got := session.InitializeResult().Instructions; !strings.Contains(got, "onfly_xray") || !strings.Contains(got, "get_tests") {
		t.Fatalf("instructions must route to the graph tools: %q", got)
	}
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range res.Tools {
		seen[tool.Name] = true
		if tool.Description == "" || len(tool.Description) > 250 {
			t.Errorf("%s: description length %d", tool.Name, len(tool.Description))
		}
	}
	for _, name := range []string{"search", "get_tests", "get_history", "graph", "find"} {
		if !seen[name] {
			t.Errorf("tool %s is not registered", name)
		}
	}
}
