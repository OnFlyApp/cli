package query

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

const (
	DefaultLimit   = 10
	MaxLimit       = 50
	keywordPool    = 20
	rrfK           = 60
	EmbedCacheSize = 256
)

func ClampLimit(n int) int {
	if n < 1 {
		return DefaultLimit
	}
	if n > MaxLimit {
		return MaxLimit
	}
	return n
}

func CandidatePool(limit int) int {
	return max(ClampLimit(limit), keywordPool)
}

// candidatePool is CandidatePool with search.candidate_pool.
func (s *Searcher) candidatePool(limit int) int {
	return max(ClampLimit(limit), orDefaultInt(s.Pool, keywordPool))
}

func orDefaultInt(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func Scope(def, arg string) string {
	switch arg {
	case "":
		return def
	case "*":
		return ""
	}
	return arg
}

type Searcher struct {
	Store                         vectorstore.Store
	Emb                           embedder.Embedder
	Logger                        *slog.Logger
	Rerank                        embedder.Reranker
	Candidates                    int
	RerankTimeout                 time.Duration
	SemanticWeight, KeywordWeight float64
	WordWeight, FieldWeight       *float64
	RRFK                          int
	SummaryKeyword                bool
	AltEmb                        embedder.Embedder
	AltWeight                     *float64
	SummaryWordWeight             *float64
	SectionWeight                 *float64
	TypeWeights                   map[string]float64
	FocusSemantic, FocusKeyword   *float64
	SummaryWeight                 *float64
	SummarySymbols, Pool          int
	Root                          string
	Expander                      *Expander
}

type Request struct {
	Query string
	// CodeOnly leaves documentation files out of the search unless the query asks
	// for documentation.
	CodeOnly     bool
	Limit        int
	Workspace    string
	ChangedSince string
	Type         string
	Path         string
	// LexicalOnly skips the embedder: the words are matched by name, text and path.
	LexicalOnly bool
}

func (s *Searcher) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Logger
}

func (s *Searcher) Search(ctx context.Context, q string, limit int, workspace string) ([]parser.Chunk, error) {
	return s.Run(ctx, Request{Query: q, Limit: limit, Workspace: workspace})
}

func orDefault(w, def float64) float64 {
	if w <= 0 {
		return def
	}
	return w
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "how": true, "does": true, "where": true, "what": true,
	"which": true, "that": true, "this": true, "with": true, "from": true, "when": true, "who": true, "why": true,
	"can": true, "not": true, "into": true, "file": true, "files": true, "code": true, "get": true, "all": true,
}

func queryWords(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	}) {
		lw := strings.ToLower(w)
		if len(lw) < 3 || stopWords[lw] || seen[lw] {
			continue
		}
		seen[lw] = true
		out = append(out, w)
		if len(out) == maxQueryWords {
			break
		}
	}
	// An identifier in the question (rateLimiter, max_token_budget) also
	// brings its parts, so that the words it is made of find code that spells
	// them differently. The parts come after every whole word.
	for _, w := range slices.Clone(out) {
		parts := splitWords(w)
		if len(parts) < 2 {
			continue
		}
		for _, p := range parts {
			if len(p) < 3 || stopWords[p] || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
			if len(out) == maxQueryWords+maxPartWords {
				return out
			}
		}
	}
	return out
}

const (
	maxQueryWords     = 6
	maxPartWords      = 4
	defaultWordWeight = 0.25
)

func (s *Searcher) wordMatches(ctx context.Context, words []string, pool int, workspace string, weights map[string]float64) []parser.Chunk {
	return s.wordMatchesWith(ctx, words, pool, workspace, s.Store.SearchKeyword, weights)
}

func (s *Searcher) wordMatchesWith(ctx context.Context, words []string, pool int, workspace string,
	lookup func(context.Context, string, int, string) ([]parser.Chunk, error), weights map[string]float64) []parser.Chunk {
	type acc struct {
		chunk parser.Chunk
		score float64
	}
	by := map[string]*acc{}
	var order []string
	for _, w := range words {
		hits, err := lookup(ctx, w, pool, workspace)
		if err != nil {
			continue
		}
		for rank, c := range hits {
			key := ChunkKey(c)
			a := by[key]
			if a == nil {
				a = &acc{chunk: c}
				by[key] = a
				order = append(order, key)
			}
			a.score += weightOf(weights, w) / float64(rrfK+rank+1)
		}
	}
	out := make([]parser.Chunk, 0, len(order))
	for _, k := range order {
		out = append(out, by[k].chunk)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := by[ChunkKey(out[i])].score, by[ChunkKey(out[j])].score
		if si != sj {
			return si > sj
		}
		return out[i].FilePath < out[j].FilePath
	})
	if len(out) > pool {
		out = out[:pool]
	}
	return out
}

func filterChunks(in []parser.Chunk, keep func(parser.Chunk) bool) []parser.Chunk {
	var out []parser.Chunk
	for _, c := range in {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

func Open(ctx context.Context, cfg config.Config) (*Searcher, func() error, error) {
	store, err := vectorstore.New(ctx, cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	if err := vectorstore.InitAll(ctx, store, cfg); err != nil {
		store.Close()
		return nil, nil, err
	}
	emb, err := embedder.New(cfg.Embedder)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	rr, err := embedder.NewReranker(cfg.Reranker)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	s, err := NewSearcher(cfg, store, emb, rr, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return s, store.Close, nil
}

func NewSearcher(cfg config.Config, store vectorstore.Store, emb embedder.Embedder, rr embedder.Reranker, logger *slog.Logger) (*Searcher, error) {
	s := &Searcher{Store: store, Emb: embedder.Cached(embedder.WithBreaker(emb, time.Minute), EmbedCacheSize), Rerank: rr, Candidates: cfg.Reranker.Candidates,
		RerankTimeout:  cfg.Reranker.Timeout(),
		SemanticWeight: cfg.Search.SemanticWeight, KeywordWeight: cfg.Search.KeywordWeight,
		WordWeight: cfg.Search.WordWeight, FieldWeight: cfg.Search.FieldWeight, RRFK: cfg.Search.RRFK,
		Root:   cfg.ProjectRoot,
		Logger: logger}
	s.SummaryKeyword = cfg.Indexer.Summaries.Provider != ""
	s.AltWeight, s.SummaryWordWeight = cfg.Search.AltWeight, cfg.Search.SummaryWordWeight
	s.SectionWeight = cfg.Search.SectionWeight
	s.FocusSemantic, s.FocusKeyword, s.SummaryWeight = cfg.Search.FocusSemanticWeight, cfg.Search.FocusKeywordWeight, cfg.Search.FileSummaryWeight
	s.SummarySymbols, s.Pool = cfg.Search.SummarySymbols, cfg.Search.CandidatePool
	if len(cfg.Search.TypeWeights) > 0 {
		s.TypeWeights = make(map[string]float64, len(cfg.Search.TypeWeights))
		for t, w := range cfg.Search.TypeWeights {
			s.TypeWeights[strings.ToLower(strings.TrimSpace(t))] = w
		}
	}
	if cfg.Search.Expansion.Provider != "" {
		chat, err := embedder.NewChat(cfg.Search.Expansion)
		if err != nil {
			return nil, fmt.Errorf("search.expansion: %w", err)
		}
		s.Expander = &Expander{Chat: chat, Timeout: cfg.Search.Expansion.Timeout()}
	}
	if cfg.Embedder.Alt != nil {
		alt, err := embedder.New(*cfg.Embedder.Alt)
		if err != nil {
			return nil, fmt.Errorf("embedder.alt: %w", err)
		}
		s.AltEmb = embedder.Cached(alt, EmbedCacheSize)
	}
	return s, nil
}

func FuseWeighted(weights []float64, lists ...[]parser.Chunk) []parser.Chunk {
	return chunksOf(fuseScored(rrfK, weights, lists...))
}

type scored struct {
	chunk parser.Chunk
	score float64
}

func chunksOf(in []scored) []parser.Chunk {
	out := make([]parser.Chunk, len(in))
	for i, x := range in {
		out[i] = x.chunk
	}
	return out
}

func fuseScored(k int, weights []float64, lists ...[]parser.Chunk) []scored {
	return fuseTyped(k, weights, nil, lists...)
}

func fuseTyped(k int, weights []float64, tw func(parser.Chunk) float64, lists ...[]parser.Chunk) []scored {
	var order []string
	by := map[string]*scored{}
	for li, list := range lists {
		w := 1.0
		if li < len(weights) {
			w = weights[li]
		}
		seen := map[string]bool{}
		rank := 0.0
		for _, ch := range list {
			key := ChunkKey(ch)
			if seen[key] {
				continue
			}
			seen[key] = true
			cw := 1.0
			if tw != nil {
				cw = tw(ch)
			}
			rank += cw
			item := by[key]
			if item == nil {
				item = &scored{chunk: ch}
				by[key] = item
				order = append(order, key)
			}
			item.score += w * cw / (float64(k) + rank)
		}
	}
	out := make([]scored, 0, len(order))
	for _, key := range order {
		out = append(out, *by[key])
	}
	sortScored(out)
	return out
}

func sortScored(out []scored) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].chunk.FilePath != out[j].chunk.FilePath {
			return out[i].chunk.FilePath < out[j].chunk.FilePath
		}
		return out[i].chunk.Content < out[j].chunk.Content
	})
}

func ChunkKey(c parser.Chunk) string {
	m := c.Metadata
	return c.FilePath + "\x00" + c.Type + "\x00" + m["name"] + "\x00" + m["part"] + "\x00" + m["start_line"] + "\x00" + c.Content
}

const (
	defaultRerankPool    = 30
	defaultRerankTimeout = 5 * time.Second
)

func (s *Searcher) rerank(ctx context.Context, q string, fused []parser.Chunk) []parser.Chunk {
	if s.Rerank == nil || len(fused) < 2 {
		return fused
	}
	pool := s.Candidates
	if pool <= 0 {
		pool = defaultRerankPool
	}
	head := fused
	if len(head) > pool {
		head = fused[:pool]
	}
	docs := make([]string, len(head))
	for i, c := range head {
		docs[i] = c.Content
	}
	timeout := s.RerankTimeout
	if timeout <= 0 {
		timeout = defaultRerankTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	order, err := s.Rerank.Rerank(rctx, q, docs, len(head))
	if err != nil {
		s.logger().Warn("rerank", "err", err)
		return fused
	}
	used := make([]bool, len(head))
	out := make([]parser.Chunk, 0, len(fused))
	for _, i := range order {
		if i < 0 || i >= len(head) || used[i] {
			continue
		}
		used[i] = true
		out = append(out, head[i])
	}
	for i, c := range head {
		if !used[i] {
			out = append(out, c)
		}
	}
	return append(out, fused[len(head):]...)
}
