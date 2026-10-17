package query

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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

type stubRerank struct {
	order []int
	err   error
}

func (r stubRerank) Rerank(context.Context, string, []string, int) ([]int, error) {
	return r.order, r.err
}

func TestRerankReordersFusedHits(t *testing.T) {
	s := newSearcher(t)
	s.Rerank = stubRerank{order: []int{1, 0}}
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "b.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRerankErrorKeepsFusedOrder(t *testing.T) {
	s := newSearcher(t)
	s.Rerank = stubRerank{err: context.DeadlineExceeded}
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "a.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRerankIgnoresOutOfRangeAndDuplicateIndices(t *testing.T) {
	s := newSearcher(t)
	s.Rerank = stubRerank{order: []int{7, 1, 1, -1}}
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "b.go" || got[1].FilePath != "a.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

type failKeyword struct{ vectorstore.Store }

func (failKeyword) SearchKeyword(context.Context, string, int, string) ([]parser.Chunk, error) {
	return nil, errors.New("fts broken")
}

func TestSearchFallsBackToSemanticWhenKeywordFails(t *testing.T) {
	s := newSearcher(t)
	s.Store = failKeyword{s.Store}
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

type blockRerank struct{}

func (blockRerank) Rerank(ctx context.Context, _ string, _ []string, _ int) ([]int, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRerankTimeoutKeepsFusedOrder(t *testing.T) {
	s := newSearcher(t)
	s.Rerank = blockRerank{}
	s.RerankTimeout = 50 * time.Millisecond
	start := time.Now()
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "a.go" || time.Since(start) > 2*time.Second {
		t.Fatalf("%+v %v %v", got, err, time.Since(start))
	}
}

func TestSearchWeightsShiftTheRanking(t *testing.T) {
	s := newSearcher(t)
	s.KeywordWeight = 0.001
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "b.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

type failEmb struct{}

func (failEmb) Embed(context.Context, string) ([]float32, error) { return nil, errors.New("down") }
func (failEmb) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("down")
}

func TestSearchDegradesToKeywordWhenEmbedderFails(t *testing.T) {
	s := newSearcher(t)
	s.Emb = failEmb{}
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 1 || got[0].FilePath != "a.go" {
		t.Fatalf("%+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, "getUserId", 5, ""); err == nil {
		t.Fatal("a cancelled context must still fail")
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestChangedSinceKeepsOnlyChangedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package p // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSearcher(t)
	s.Root = root
	got, err := s.Run(context.Background(), Request{Query: "getUserId", Limit: 5, ChangedSince: "HEAD"})
	if err != nil || len(got) != 1 || got[0].FilePath != "b.go" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.Run(context.Background(), Request{Query: "x", ChangedSince: "--output=/tmp/x"}); err == nil {
		t.Fatal("a ref starting with - must be rejected")
	}
}

func TestQueryWordsDropsStopWordsAndShortWords(t *testing.T) {
	got := queryWords("How does the retry on 429 work in getUserId, retry?")
	want := []string{"retry", "429", "work", "getUserId"}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v, want %v", got, want)
		}
	}
	if len(queryWords("a b c d e f g h i j k l m n o p alpha beta gamma delta epsilon zeta eta theta")) != maxQueryWords {
		t.Fatal("words must be capped")
	}
}

func TestWordMatchesRanksByHowManyWordsMatch(t *testing.T) {
	s := newSearcher(t)
	got := s.wordMatches(context.Background(), queryWords("getUserId func"), 10, "", nil)
	if len(got) != 2 || got[0].FilePath != "a.go" || got[1].FilePath != "b.go" {
		t.Fatalf("the chunk with both words must come first, the one with only %q after it: %+v", "func", got)
	}
	g, err := s.Gather(context.Background(), Request{Query: "single", Limit: 5})
	if err != nil || g.Words != nil {
		t.Fatalf("one word is left to the whole-query keyword search: %v %v", g, err)
	}
}

func TestSearchFiltersByTypeAndPath(t *testing.T) {
	s := newSearcher(t)
	ctx := context.Background()
	got, err := s.Run(ctx, Request{Query: "getUserId", Limit: 5, Type: "FUNCTION"})
	if err != nil || len(got) != 2 {
		t.Fatalf("type is case-insensitive and matches both: %+v %v", got, err)
	}
	got, err = s.Run(ctx, Request{Query: "getUserId", Limit: 5, Type: "section"})
	if err != nil || len(got) != 0 {
		t.Fatalf("no sections: %+v %v", got, err)
	}
	got, err = s.Run(ctx, Request{Query: "getUserId", Limit: 5, Path: "b.*"})
	if err != nil || len(got) != 1 || got[0].FilePath != "b.go" {
		t.Fatalf("path glob: %+v %v", got, err)
	}
	if _, err := s.Run(ctx, Request{Query: "x", Path: "["}); err == nil {
		t.Fatal("a bad glob must be an error")
	}
}
