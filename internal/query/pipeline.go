package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/axitdev/onfly/internal/gitx"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/bmatcuk/doublestar/v4"
)

const (
	slotAlt = "alt"
)

const (
	typeWeightPoolFactor = 3
	typeWeightPoolMax    = 150

	defaultSemanticWeight    = 2.0
	defaultKeywordWeight     = 0.5
	defaultFieldWeight       = 0.5
	defaultRRFK              = 20
	defaultAltWeight         = 1.0
	defaultSummaryWordWeight = 0.125
)

type Params struct {
	Semantic, Keyword, Word, Field float64
	Alt, SummaryWord               float64
	Section                        float64
	RRFK                           int
}

func (s *Searcher) Params() Params {
	p := Params{
		Semantic:    orDefault(s.SemanticWeight, defaultSemanticWeight),
		Keyword:     orDefault(s.KeywordWeight, defaultKeywordWeight),
		Word:        defaultWordWeight,
		Field:       defaultFieldWeight,
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

type Gathered struct {
	Query             string
	Terms             []string
	Semantic          []parser.Chunk
	Keyword           []parser.Chunk
	Words             []parser.Chunk
	Recent            []parser.Chunk
	Alt, SummaryWords []parser.Chunk
	// Weights is how rare each query word is in the index, from 0 to 1.
	Weights map[string]float64
	// CodeOnly says documentation files were left out of every list.
	CodeOnly bool
	req      Request
}

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
			if ok, _ := doublestar.Match(r.Path, c.FilePath); !ok {
				return false
			}
		}
		return true
	}
	filtered := only != nil || r.Type != "" || r.Path != ""
	if filtered {
		pool = MaxLimit
	}
	if len(s.TypeWeights) > 0 {
		pool = max(pool, min(pool*typeWeightPoolFactor, typeWeightPoolMax))
	}
	g := &Gathered{Query: r.Query, Terms: queryWords(r.Query), req: r}
	if r.CodeOnly && !asksForDocs(g.Terms) {
		ctx, g.CodeOnly = vectorstore.WithoutProse(ctx), true
	}
	g.Weights = s.termWeights(ctx, g.Terms, r.Workspace)

	vec, embedErr := s.Emb.Embed(ctx, r.Query)
	if embedErr == nil {
		var err error
		semPool := pool
		if g.CodeOnly {
			semPool = pool * 3 // some of the nearest are prose and go
		}
		if g.Semantic, err = s.Store.SearchSemantic(ctx, vec, semPool, r.Workspace); err != nil {
			return nil, err
		}
		if g.CodeOnly {
			g.Semantic = filterChunks(g.Semantic, func(c parser.Chunk) bool { return !vectorstore.IsProse(c.FilePath) })
			if len(g.Semantic) > pool {
				g.Semantic = g.Semantic[:pool]
			}
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
	if len(g.Terms) >= 2 {
		g.Words = s.wordMatches(ctx, g.Terms, pool, r.Workspace, g.Weights)
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
		g.SummaryWords = s.wordMatchesWith(ctx, g.Terms, pool, r.Workspace, s.Store.SearchSummary, g.Weights)
	}
	if filtered {
		g.Semantic, g.Keyword, g.Words = filterChunks(g.Semantic, keep), filterChunks(g.Keyword, keep), filterChunks(g.Words, keep)
		g.Alt, g.SummaryWords = filterChunks(g.Alt, keep), filterChunks(g.SummaryWords, keep)
	}
	return g, nil
}

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
		if f := fieldMatches(g.Terms, cands, g.Weights); len(f) > 0 {
			lists, weights = append(lists, f), append(weights, p.Field*p.Keyword)
		}
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
