package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/gitx"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/usage"
	"github.com/bmatcuk/doublestar/v4"
)

// Defaults measured with `onfly eval tune` on 573 cases (45 hand-written, 528
// generated), fitted on half and checked on the other half: MRR 0.605 -> 0.678
// on the held-out half against the old semantic=1 keyword=1 field=off rrf_k=60.
const (
	slotAlt = "alt"
)

const (
	typeWeightPoolFactor = 3
	typeWeightPoolMax    = 150

	defaultSemanticWeight = 2.0
	defaultKeywordWeight  = 0.5
	defaultFieldWeight    = 0.5
	defaultRRFK           = 20
	defaultAltWeight      = 1.0
	// defaultSummaryWordWeight is absolute (the per-word list is relative to the keyword weight).
	defaultSummaryWordWeight = 0.125
)

// Params are the numbers that turn gathered candidate lists into one ranking.
// Tuning varies them without touching the store or the embedder again.
type Params struct {
	Semantic, Keyword, Word, Field, Recency float64
	// Alt and SummaryWord weigh the second-embedder and summary-keyword lists.
	Alt, SummaryWord float64
	// Section multiplies the score of documentation sections (1 is neutral).
	Section float64
	RRFK    int
}

// Params returns the searcher's effective fusion numbers.
func (s *Searcher) Params() Params {
	p := Params{
		Semantic:    orDefault(s.SemanticWeight, defaultSemanticWeight),
		Keyword:     orDefault(s.KeywordWeight, defaultKeywordWeight),
		Word:        defaultWordWeight,
		Field:       defaultFieldWeight,
		Recency:     s.RecencyWeight,
		Alt:         defaultAltWeight,
		SummaryWord: defaultSummaryWordWeight,
		Section:     1,
		RRFK:        s.RRFK,
	}
	if s.WordWeight != nil {
		p.Word = *s.WordWeight
	}
	if s.FieldWeight != nil {
		p.Field = *s.FieldWeight
	}
	if s.SectionWeight != nil {
		p.Section = *s.SectionWeight
	}
	if s.AltWeight != nil {
		p.Alt = *s.AltWeight
	}
	if s.SummaryWordWeight != nil {
		p.SummaryWord = *s.SummaryWordWeight
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
	Recent   []parser.Chunk
	// Alt and SummaryWords come from the second embedder and the summary
	// keyword index; empty when those are not in use.
	Alt, SummaryWords []parser.Chunk
	req               Request
}

// Run embeds the query, merges semantic, keyword and name lists and reranks. When the embedder fails it answers
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
	if len(s.TypeWeights) > 0 {
		// Demoted chunks still fill candidate slots; read deeper so real
		// definitions behind them make it into the lists.
		pool = max(pool, min(pool*typeWeightPoolFactor, typeWeightPoolMax))
	}
	g := &Gathered{Query: r.Query, Terms: queryWords(r.Query), req: r}

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
	if s.AltEmb != nil {
		if av, err := s.AltEmb.Embed(ctx, r.Query); err != nil {
			s.logger().Warn("alt embedder", "err", err)
		} else if got, err := s.Store.SearchExtra(ctx, slotAlt, av, pool, r.Workspace); err != nil {
			s.logger().Warn("alt vectors", "err", err)
		} else {
			g.Alt = got
		}
	}
	if s.SummaryKeyword && len(g.Terms) > 0 {
		g.SummaryWords = s.wordMatchesWith(ctx, g.Terms, pool, r.Workspace, s.Store.SearchSummary)
	}
	if filtered {
		g.Semantic, g.Keyword, g.Words = filterChunks(g.Semantic, keep), filterChunks(g.Keyword, keep), filterChunks(g.Words, keep)
		g.Alt, g.SummaryWords = filterChunks(g.Alt, keep), filterChunks(g.SummaryWords, keep)
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
	if len(g.Alt) > 0 && p.Alt > 0 {
		lists, weights = append(lists, g.Alt), append(weights, p.Alt)
	}
	if len(g.SummaryWords) > 0 && p.SummaryWord > 0 {
		lists, weights = append(lists, g.SummaryWords), append(weights, p.SummaryWord)
	}
	if p.Field > 0 {
		cands := union(g.Semantic, g.Keyword, g.Words, g.Alt, g.SummaryWords)
		if f := fieldMatches(g.Terms, cands); len(f) > 0 {
			lists, weights = append(lists, f), append(weights, p.Field*p.Keyword)
		}
	}
	if s.UsageWeight > 0 && s.Usage != nil {
		if u := usedChunks(s.Usage.Counts(), union(g.Semantic, g.Keyword, g.Words)); len(u) > 0 {
			lists, weights = append(lists, u), append(weights, s.UsageWeight)
		}
	}
	if len(g.Recent) > 0 && p.Recency > 0 {
		lists, weights = append(lists, g.Recent), append(weights, p.Recency)
	}
	var typeW func(parser.Chunk) float64
	if len(s.TypeWeights) > 0 {
		typeW = func(c parser.Chunk) float64 {
			if w, ok := s.TypeWeights[strings.ToLower(c.Type)]; ok {
				return w
			}
			return 1
		}
	}
	ranked := fuseTyped(p.RRFK, weights, typeW, lists...)
	if p.Section != 1 && p.Section > 0 {
		for i := range ranked {
			if ranked[i].chunk.Type == "section" {
				ranked[i].score *= p.Section
			}
		}
		sortScored(ranked)
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

// usedChunks orders candidates that were opened before by how often.
func usedChunks(counts map[string]int, cands []parser.Chunk) []parser.Chunk {
	if len(counts) == 0 {
		return nil
	}
	type item struct {
		c parser.Chunk
		n int
	}
	var items []item
	for _, c := range cands {
		if n := counts[usage.Key(c.FilePath, c.Metadata["name"])]; n > 0 {
			items = append(items, item{c, n})
		}
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].n > items[b].n })
	out := make([]parser.Chunk, len(items))
	for i, it := range items {
		out[i] = it.c
	}
	return out
}
