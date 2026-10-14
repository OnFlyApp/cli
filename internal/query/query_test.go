package query

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

type vecEmb struct{ v []float32 }

func (e vecEmb) Embed(context.Context, string) ([]float32, error) { return e.v, nil }
func (e vecEmb) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = e.v
	}
	return out, nil
}

func newSearcher(t *testing.T) *Searcher {
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
	exact := parser.Chunk{FilePath: "a.go", Type: "function", Content: "func getUserId() {}", Metadata: map[string]string{"name": "getUserId"}}
	near := parser.Chunk{FilePath: "b.go", Type: "function", Content: "func other() {}", Metadata: map[string]string{"name": "other"}}
	if err := storetest.Save(ctx, st, []vectorstore.EmbeddedChunk{
		{Chunk: exact, Vector: []float32{0, 1, 0, 0}},
		{Chunk: near, Vector: []float32{1, 0, 0, 0}},
	}); err != nil {
		t.Fatal(err)
	}
	return &Searcher{Store: st, Emb: vecEmb{v: []float32{1, 0, 0, 0}}}
}

func TestSearchFusesBothRankings(t *testing.T) {
	got, err := newSearcher(t).Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "a.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSearchSurvivesBadKeywordQuery(t *testing.T) {
	got, err := newSearcher(t).Search(context.Background(), `getUserId "quote`, 5, "")
	if err != nil || len(got) == 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestScope(t *testing.T) {
	if Scope("ws", "") != "ws" || Scope("ws", "*") != "" || Scope("ws", "other") != "other" {
		t.Fatal("scope")
	}
}
