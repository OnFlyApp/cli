package query

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

func summaryStore(t *testing.T) *Searcher {
	t.Helper()
	st, err := vectorstore.OpenSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	fn := func(file, name, body string) vectorstore.EmbeddedChunk {
		return vectorstore.EmbeddedChunk{
			Chunk:  parser.Chunk{FilePath: file, Type: "method", Content: "func " + name + "() {" + body + "}", Metadata: map[string]string{"name": name}},
			Vector: []float32{0, 0, 1, 0},
		}
	}
	chunks := []vectorstore.EmbeddedChunk{
		fn("pay/calc.go", "calculate", strings.Repeat(" x := 1;", 30)),
		fn("pay/calc.go", "round", " return 1 "),
		fn("pay/calc.go", "cap", strings.Repeat(" y := 2;", 10)),
		fn("pay/calc.go", "tiny", ""),
		{Chunk: parser.Chunk{FilePath: "pay/calc.go", Type: parser.FileSummaryType, Content: "File pay/calc.go\nDefines: calculate, round, cap, tiny", Metadata: map[string]string{}}, Vector: []float32{1, 0, 0, 0}},
		fn("other/x.go", "unrelated", " return 2 "),
	}
	if err := storetest.Save(ctx, st, chunks); err != nil {
		t.Fatal(err)
	}
	return &Searcher{Store: st}
}

func TestFileSummaryIsReplacedByTheLargestSymbolOfItsFile(t *testing.T) {
	s := summaryStore(t)
	rows, _ := s.Store.ChunksByFiles(context.Background(), []string{"pay/calc.go", "other/x.go"})
	var sum, other parser.Chunk
	for _, r := range rows {
		switch {
		case r.Chunk.Type == parser.FileSummaryType:
			sum = r.Chunk
		case r.Chunk.FilePath == "other/x.go":
			other = r.Chunk
		}
	}
	got := s.expandFileSummaries(context.Background(), []parser.Chunk{other, sum})
	var names []string
	for _, c := range got {
		if c.Type == parser.FileSummaryType {
			t.Fatalf("the summary must not reach the result: %+v", got)
		}
		names = append(names, c.Metadata["name"])
	}
	if want := "unrelated calculate"; strings.Join(names, " ") != want {
		t.Fatalf("%v, want %s: the largest symbol, at the summary's place", names, want)
	}
}

func TestFileSummaryPrefersSymbolsTheRankingAlreadyHas(t *testing.T) {
	s := summaryStore(t)
	rows, _ := s.Store.ChunksByFiles(context.Background(), []string{"pay/calc.go"})
	var sum, tiny, round parser.Chunk
	for _, r := range rows {
		switch r.Chunk.Metadata["name"] {
		case "tiny":
			tiny = r.Chunk
		case "round":
			round = r.Chunk
		}
		if r.Chunk.Type == parser.FileSummaryType {
			sum = r.Chunk
		}
	}
	got := s.expandFileSummaries(context.Background(), []parser.Chunk{sum, tiny, round})
	if len(got) != 2 || got[0].Metadata["name"] != "tiny" || got[1].Metadata["name"] != "round" {
		t.Fatalf("symbols that matched on their own come first and are not listed twice: %+v", got)
	}
	if plain := []parser.Chunk{tiny}; len(s.expandFileSummaries(context.Background(), plain)) != 1 {
		t.Fatal("a ranking without summaries is returned as it is")
	}
}
