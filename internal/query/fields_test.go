package query

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

func TestSplitWords(t *testing.T) {
	cases := map[string][]string{
		"getUserID":        {"get", "user", "id"},
		"parse_http_url":   {"parse", "http", "url"},
		"HTTPServer":       {"http", "server"},
		"internal/index/x": {"internal", "index", "x"},
		"":                 nil,
	}
	for in, want := range cases {
		got := splitWords(in)
		if len(got) != len(want) {
			t.Errorf("%q: %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: %v, want %v", in, got, want)
			}
		}
	}
}

func TestSameWordStems(t *testing.T) {
	yes := [][2]string{{"retry", "retries"}, {"index", "indexing"}, {"embed", "embedder"}, {"user", "users"}}
	for _, p := range yes {
		if !sameWord(p[0], p[1]) {
			t.Errorf("%s ~ %s", p[0], p[1])
		}
	}
	if sameWord("get", "getter") && len("get") < minStemLen {
		t.Error("short words must match exactly")
	}
	if sameWord("cache", "cash") || sameWord("read", "write") {
		t.Error("unrelated words matched")
	}
}

func TestFieldMatchesRanksNamedChunkFirst(t *testing.T) {
	mentions := parser.Chunk{FilePath: "docs/a.md", Type: "section", Content: "we call prune whenever files vanish", Metadata: map[string]string{"name": "Intro"}}
	named := parser.Chunk{FilePath: "internal/index/index.go", Type: "method", Content: "func (s *Session) prune() {}", Metadata: map[string]string{"name": "prune"}}
	other := parser.Chunk{FilePath: "x.go", Type: "function", Content: "func other() {}", Metadata: map[string]string{"name": "other"}}
	got := fieldMatches([]string{"prune", "files"}, []parser.Chunk{mentions, other, named})
	if len(got) != 2 || got[0].Metadata["name"] != "prune" {
		t.Fatalf("the chunk named for the query must lead; chunks with no field hit are dropped: %+v", got)
	}
}

func TestFieldMatchesUsesPathCommentAndSummary(t *testing.T) {
	byPath := parser.Chunk{FilePath: "internal/watch/watcher.go", Type: "function", Content: "func run() {}", Metadata: map[string]string{"name": "run"}}
	byDoc := parser.Chunk{FilePath: "z.go", Type: "function", Content: "// Debounce coalesces events.\nfunc z() {}", Metadata: map[string]string{"name": "z"}}
	bySummary := parser.Chunk{FilePath: "y.go", Type: "function", Content: "func y() {}", Metadata: map[string]string{"name": "y", "summary": "Retries failed requests with backoff."}}
	for q, want := range map[string]string{"watcher": "run", "debounce": "z", "backoff": "y"} {
		got := fieldMatches([]string{q}, []parser.Chunk{byPath, byDoc, bySummary})
		if len(got) == 0 || got[0].Metadata["name"] != want {
			t.Errorf("%s: %+v, want %s first", q, got, want)
		}
	}
	if fieldMatches(nil, []parser.Chunk{byPath}) != nil {
		t.Error("no terms, no list")
	}
}

func TestLeadingComment(t *testing.T) {
	got := leadingComment("// First line.\n// Second line.\nfunc f() {}\n// not me")
	if got != "First line. Second line. " {
		t.Fatalf("%q", got)
	}
	if leadingComment("func f() {}") == "" {
		// the first line of a chunk without a comment still counts (a doc heading)
		t.Log("ok")
	}
}

// searcherWith builds a searcher over the given chunks; every chunk shares one
// vector so ranking comes from the keyword and name lists.
func searcherWith(t *testing.T, chunks ...parser.Chunk) *Searcher {
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
	var emb []vectorstore.EmbeddedChunk
	for _, c := range chunks {
		emb = append(emb, vectorstore.EmbeddedChunk{Chunk: c, Vector: []float32{1, 0, 0, 0}})
	}
	if err := storetest.Save(ctx, st, emb); err != nil {
		t.Fatal(err)
	}
	return &Searcher{Store: st, Emb: vecEmb{v: []float32{1, 0, 0, 0}}}
}

func TestFieldListLiftsNamedChunk(t *testing.T) {
	// "handler" appears in prose in the first chunk; the second is the handler.
	prose := parser.Chunk{FilePath: "a.go", Type: "function", Content: "func a() { /* the handler of every request is elsewhere */ }", Metadata: map[string]string{"name": "a"}}
	named := parser.Chunk{FilePath: "b.go", Type: "function", Content: "func requestHandler() {}", Metadata: map[string]string{"name": "requestHandler"}}
	s := searcherWith(t, prose, named)
	zero := 0.0
	s.FieldWeight = &zero
	off, err := s.Search(context.Background(), "request handler", 2, "")
	if err != nil || len(off) != 2 {
		t.Fatalf("%+v %v", off, err)
	}
	on := 1.0
	s.FieldWeight = &on
	got, err := s.Search(context.Background(), "request handler", 2, "")
	if err != nil || got[0].Metadata["name"] != "requestHandler" {
		t.Fatalf("name list must lead when weighted: %+v %v", got, err)
	}
}
