package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/axitdev/onfly/internal/gitx"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/bmatcuk/doublestar/v4"
)

// Defaults measured with `onfly eval tune` on 573 cases (45 hand-written, 528
// generated), fitted on half and checked on the other half: MRR 0.605 -> 0.678
// on the held-out half against the old semantic=1 keyword=1 field=off rrf_k=60.
const (
	defaultSemanticWeight = 2.0
	defaultKeywordWeight  = 0.5
	defaultFieldWeight    = 0.5
	defaultRRFK           = 20
	defaultExpandWeight   = 0.5
	// secondChunkBonus is how much a file's second-best chunk adds to its score when grouping.
	secondChunkBonus = 0.1
)

// Params are the numbers that turn gathered candidate lists into one ranking.
// Tuning varies them without touching the store or the embedder again.
type Params struct {
	Semantic, Keyword, Word, Field, Recency, Expand float64
	RRFK                                            int
}

// Params returns the searcher's effective fusion numbers.
func (s *Searcher) Params() Params {
	p := Params{
		Semantic: orDefault(s.SemanticWeight, defaultSemanticWeight),
		Keyword:  orDefault(s.KeywordWeight, defaultKeywordWeight),
		Word:     defaultWordWeight,
		Field:    defaultFieldWeight,
		Recency:  s.RecencyWeight,
		Expand:   defaultExpandWeight,
		RRFK:     s.RRFK,
	}
	if s.WordWeight != nil {
		p.Word = *s.WordWeight
	}
	if s.FieldWeight != nil {
		p.Field = *s.FieldWeight
	}
	if s.ExpandWeight > 0 {
		p.Expand = s.ExpandWeight
	}
	if p.RRFK <= 0 {
		p.RRFK = defaultRRFK
	}
	return p
}

// Gathered holds every candidate list for one query, filtered but not yet fused.
type Gathered struct {
	Query string
	// Terms are the content words of the query, used by the name/path/doc list.
	Terms    []string
	Semantic []parser.Chunk
	Keyword  []parser.Chunk
	Words    []parser.Chunk
	// ExpandSemantic and ExpandWords come from query expansion; both are empty when it is off.
	ExpandSemantic []parser.Chunk
	ExpandWords    []parser.Chunk
	Recent         []parser.Chunk
	req            Request
}

// Run embeds the query, merges semantic, keyword, name and expansion lists,
// optionally groups by file, and reranks. When the embedder fails it answers
// from keyword results alone.
func (s *Searcher) Run(ctx context.Context, r Request) ([]parser.Chunk, error) {
	g, err := s.Gather(ctx, r)
	if err != nil {
		return nil, err
	}
	fused := s.rerank(ctx, r.Query, s.Fuse(g, s.Params()))
	if n := ClampLimit(r.Limit); len(fused) > n {
		fused = fused[:n]
	}
	return fused, nil
}

// Gather runs the store and embedder lookups for r.
func (s *Searcher) Gather(ctx context.Context, r Request) (*Gathered, error) {
	var only map[string]bool
	pool := CandidatePool(r.Limit)
	if r.Path != "" && !doublestar.ValidatePattern(r.Path) {
		return nil, fmt.Errorf("invalid path pattern %q", r.Path)
	}
	if r.GroupBy != "" && r.GroupBy != "file" && r.GroupBy != "chunk" {
		return nil, fmt.Errorf("group_by must be file or chunk, got %q", r.GroupBy)
	}
	if r.ChangedSince != "" {
		if s.Root == "" {
			return nil, fmt.Errorf("changed_since needs a project root")
		}
		files, err := gitx.ChangedSince(ctx, s.Root, r.ChangedSince)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return &Gathered{Query: r.Query, req: r}, nil
		}
		only = make(map[string]bool, len(files))
		for _, f := range files {
			only[f] = true
		}
	}
	keep := func(c parser.Chunk) bool {
		if only != nil && !only[c.FilePath] {
			return false
		}
		if r.Type != "" && !strings.EqualFold(c.Type, r.Type) {
			return false
		}
		if r.Path != "" {
			if ok, _ := doublestar.PathMatch(r.Path, c.FilePath); !ok {
				return false
			}
		}
		return true
	}
	filtered := only != nil || r.Type != "" || r.Path != ""
	if filtered {
		pool = MaxLimit // filters run after retrieval, so look wide
	}
	g := &Gathered{Query: r.Query, Terms: queryWords(r.Query), req: r}

	var exp *expansion
	if s.Expand != nil {
		exp = s.expand(ctx, r.Query)
	}
	vec, embedErr := s.Emb.Embed(ctx, r.Query)
	if embedErr == nil {
		var err error
		if g.Semantic, err = s.Store.SearchSemantic(ctx, vec, pool, r.Workspace); err != nil {
			return nil, err
		}
	} else if ctx.Err() != nil {
		return nil, embedErr
	}
	keyword, err := s.Store.SearchKeyword(ctx, r.Query, pool, r.Workspace)
	if err != nil {
		if embedErr != nil {
			return nil, embedErr
		}
		s.logger().Warn("search keyword", "err", err)
		keyword = nil
	}
	g.Keyword = keyword
	if embedErr != nil {
		s.logger().Warn("embedder unavailable, using keyword results only", "err", embedErr)
	}
	if len(g.Terms) >= 2 { // a single word is already the whole-query keyword search
		g.Words = s.wordMatches(ctx, g.Terms, pool, r.Workspace)
	}
	if exp != nil {
		g.ExpandSemantic, g.ExpandWords = s.expandLists(ctx, exp, pool, r.Workspace)
	}
	if filtered {
		g.Semantic, g.Keyword, g.Words = filterChunks(g.Semantic, keep), filterChunks(g.Keyword, keep), filterChunks(g.Words, keep)
		g.ExpandSemantic, g.ExpandWords = filterChunks(g.ExpandSemantic, keep), filterChunks(g.ExpandWords, keep)
	}
	if s.RecencyWeight > 0 {
		g.Recent = s.recentCandidates(ctx, g.Semantic, g.Keyword)
	}
	return g, nil
}

// Fuse turns gathered lists into one ranked list, before reranking and the limit.
func (s *Searcher) Fuse(g *Gathered, p Params) []parser.Chunk {
	lists := [][]parser.Chunk{g.Semantic, g.Keyword, g.Words}
	weights := []float64{p.Semantic, p.Keyword, p.Word * p.Keyword}
	if p.Field > 0 {
		cands := union(g.Semantic, g.Keyword, g.Words, g.ExpandSemantic, g.ExpandWords)
		if f := fieldMatches(g.Terms, cands); len(f) > 0 {
			lists, weights = append(lists, f), append(weights, p.Field*p.Keyword)
		}
	}
	if len(g.ExpandSemantic) > 0 {
		lists, weights = append(lists, g.ExpandSemantic), append(weights, p.Expand)
	}
	if len(g.ExpandWords) > 0 {
		lists, weights = append(lists, g.ExpandWords), append(weights, p.Expand*p.Word)
	}
	if len(g.Recent) > 0 && p.Recency > 0 {
		lists, weights = append(lists, g.Recent), append(weights, p.Recency)
	}
	ranked := fuseScored(p.RRFK, weights, lists...)
	group := s.GroupBy
	if g.req.GroupBy != "" {
		group = g.req.GroupBy
	}
	if s.Dedupe {
		ranked = dedupe(ranked)
	}
	if group == "file" {
		ranked = groupByFile(ranked)
	}
	return chunksOf(ranked)
}

// union is the distinct chunks of every list, in list order.
func union(lists ...[]parser.Chunk) []parser.Chunk {
	seen := map[string]bool{}
	var out []parser.Chunk
	for _, l := range lists {
		for _, c := range l {
			if k := ChunkKey(c); !seen[k] {
				seen[k] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// dedupe drops a chunk whose type, name and text repeat a higher-ranked one.
func dedupe(in []scored) []scored {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, x := range in {
		k := x.chunk.Type + "\x00" + x.chunk.Metadata["name"] + "\x00" + x.chunk.Content
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	return out
}

// groupByFile keeps the best chunk of each file and orders files by that
// chunk's score plus a small bonus for the file's second-best chunk.
func groupByFile(in []scored) []scored {
	type fileAcc struct {
		best   scored
		second float64
	}
	by := map[string]*fileAcc{}
	var order []string
	for _, x := range in {
		f := by[x.chunk.FilePath]
		if f == nil {
			by[x.chunk.FilePath] = &fileAcc{best: x}
			order = append(order, x.chunk.FilePath)
			continue
		}
		if f.second == 0 {
			f.second = x.score
		}
	}
	out := make([]scored, 0, len(order))
	for _, p := range order {
		f := by[p]
		out = append(out, scored{chunk: f.best.chunk, score: f.best.score + secondChunkBonus*f.second})
	}
	// order is already by best score; the bonus can only reorder close files
	sortScored(out)
	return out
}
