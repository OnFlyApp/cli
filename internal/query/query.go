// Package query runs one search: embed, semantic, keyword, reciprocal-rank fusion.
package query

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/gitx"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/bmatcuk/doublestar/v4"
)

const (
	DefaultLimit = 10
	MaxLimit     = 50
	keywordPool  = 20
	rrfK         = 60
	// EmbedCacheSize is how many query vectors a Searcher remembers.
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

// Scope maps a tool argument to a store workspace: "" is the default, "*" is all.
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
	Store  vectorstore.Store
	Emb    embedder.Embedder
	Logger *slog.Logger
	// Rerank, when set, reorders the top Candidates fused hits.
	Rerank     embedder.Reranker
	Candidates int
	// RerankTimeout bounds one rerank call; zero means 5s.
	RerankTimeout time.Duration
	// Fusion weights; zero semantic and keyword weights mean 1, zero recency means off.
	SemanticWeight, KeywordWeight, RecencyWeight float64
	RecencyDays                                  int
	// Root is the project root, used for git-based recency and changed-since filters.
	Root string

	recentMu   sync.Mutex
	recentAt   time.Time
	recentList []string
}

// Request is one search. ChangedSince, a git ref, keeps only files that differ from it.
type Request struct {
	Query        string
	Limit        int
	Workspace    string
	ChangedSince string
	// Type keeps one chunk type (function, method, struct, section, ...).
	Type string
	// Path keeps files matching a doublestar glob, relative to the project root.
	Path string
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

func orOne(w float64) float64 {
	if w <= 0 {
		return 1
	}
	return w
}

// Run embeds the query, merges semantic and keyword results, and reranks.
// When the embedder fails it answers from keyword results alone.
func (s *Searcher) Run(ctx context.Context, r Request) ([]parser.Chunk, error) {
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
			return nil, nil
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
	var semantic []parser.Chunk
	vec, embedErr := s.Emb.Embed(ctx, r.Query)
	if embedErr == nil {
		var err error
		if semantic, err = s.Store.SearchSemantic(ctx, vec, pool, r.Workspace); err != nil {
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
	if embedErr != nil {
		s.logger().Warn("embedder unavailable, using keyword results only", "err", embedErr)
	}
	words := s.wordMatches(ctx, r.Query, pool, r.Workspace)
	if filtered {
		semantic, keyword, words = filterChunks(semantic, keep), filterChunks(keyword, keep), filterChunks(words, keep)
	}
	lists := [][]parser.Chunk{semantic, keyword, words}
	weights := []float64{orOne(s.SemanticWeight), orOne(s.KeywordWeight), wordListWeight * orOne(s.KeywordWeight)}
	if s.RecencyWeight > 0 {
		if recent := s.recentCandidates(ctx, semantic, keyword); len(recent) > 0 {
			lists, weights = append(lists, recent), append(weights, s.RecencyWeight)
		}
	}
	fused := s.rerank(ctx, r.Query, FuseWeighted(weights, lists...))
	if n := ClampLimit(r.Limit); len(fused) > n {
		fused = fused[:n]
	}
	return fused, nil
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "how": true, "does": true, "where": true, "what": true,
	"which": true, "that": true, "this": true, "with": true, "from": true, "when": true, "who": true, "why": true,
	"can": true, "not": true, "into": true, "file": true, "files": true, "code": true, "get": true, "all": true,
}

// queryWords picks the words of a question worth a keyword lookup, in order.
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
	return out
}

const (
	maxQueryWords = 6
	// wordListWeight scales the per-word keyword list against the other lists.
	// Measured on this repo's eval set: 0.25 beat 0.1, 0.5 and 0.75 (recall@5 0.9 vs 0.8).
	wordListWeight = 0.25
)

// wordMatches runs a keyword lookup per query word and ranks chunks by how
// many words they contain, so "retry 429 embedder" favours the chunk that has
// all three. The whole-question keyword search rarely matches prose questions.
func (s *Searcher) wordMatches(ctx context.Context, q string, pool int, workspace string) []parser.Chunk {
	words := queryWords(q)
	if len(words) < 2 {
		return nil // a single word is already the whole-query keyword search
	}
	type acc struct {
		chunk parser.Chunk
		score float64
	}
	by := map[string]*acc{}
	var order []string
	for _, w := range words {
		hits, err := s.Store.SearchKeyword(ctx, w, pool, workspace)
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
			a.score += 1 / float64(rrfK+rank+1)
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

// recentCandidates orders the candidates from recently committed files by how
// recently their file changed. Git failures only turn the signal off.
func (s *Searcher) recentCandidates(ctx context.Context, cands ...[]parser.Chunk) []parser.Chunk {
	if s.Root == "" {
		return nil
	}
	s.recentMu.Lock()
	if time.Since(s.recentAt) > 30*time.Second {
		files, err := gitx.Recent(ctx, s.Root, s.RecencyDays)
		if err != nil {
			s.logger().Warn("recency", "err", err)
			files = nil
		}
		s.recentList, s.recentAt = files, time.Now()
	}
	files := s.recentList
	s.recentMu.Unlock()
	rank := make(map[string]int, len(files))
	for i, f := range files {
		rank[f] = i
	}
	var out []parser.Chunk
	seen := map[string]bool{}
	for _, list := range cands {
		for _, c := range list {
			if _, ok := rank[c.FilePath]; ok && !seen[ChunkKey(c)] {
				seen[ChunkKey(c)] = true
				out = append(out, c)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].FilePath] < rank[out[j].FilePath] })
	return out
}

// Open builds a Searcher for command-line use. Close the store with the returned func.
func Open(ctx context.Context, cfg config.Config) (*Searcher, func() error, error) {
	store, err := vectorstore.New(ctx, cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	if err := store.Init(ctx, cfg.Embedder.Dimensions); err != nil {
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
	return &Searcher{Store: store, Emb: embedder.Cached(emb, EmbedCacheSize), Rerank: rr, Candidates: cfg.Reranker.Candidates,
		RerankTimeout:  cfg.Reranker.Timeout(),
		SemanticWeight: cfg.Search.SemanticWeight, KeywordWeight: cfg.Search.KeywordWeight,
		RecencyWeight: cfg.Search.RecencyWeight, RecencyDays: cfg.Search.RecencyDays,
		Root:   cfg.ProjectRoot,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}, store.Close, nil
}

// Fuse merges ranked lists by reciprocal rank. A chunk in several lists
// appears once and outranks a chunk that is first in only one list.
func Fuse(lists ...[]parser.Chunk) []parser.Chunk {
	return FuseWeighted(nil, lists...)
}

// FuseWeighted is Fuse with a weight per list; a missing weight is 1.
func FuseWeighted(weights []float64, lists ...[]parser.Chunk) []parser.Chunk {
	type acc struct {
		chunk parser.Chunk
		score float64
	}
	var order []string
	by := map[string]*acc{}
	for li, list := range lists {
		w := 1.0
		if li < len(weights) {
			w = weights[li]
		}
		seen := map[string]bool{}
		rank := 0
		for _, ch := range list {
			key := ChunkKey(ch)
			if seen[key] {
				continue
			}
			seen[key] = true
			rank++
			item := by[key]
			if item == nil {
				item = &acc{chunk: ch}
				by[key] = item
				order = append(order, key)
			}
			item.score += w / float64(rrfK+rank)
		}
	}
	out := make([]parser.Chunk, 0, len(order))
	for _, key := range order {
		out = append(out, by[key].chunk)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := by[ChunkKey(out[i])].score, by[ChunkKey(out[j])].score
		if si != sj {
			return si > sj
		}
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		return out[i].Content < out[j].Content
	})
	return out
}

// ChunkKey identifies a chunk across result lists. Path, type, name, part,
// first line and content together are unique per row.
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
