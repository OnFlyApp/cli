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
	// Fusion weights; a zero semantic or keyword weight takes the default, zero recency means off.
	SemanticWeight, KeywordWeight, RecencyWeight float64
	RecencyDays                                  int
	// WordWeight and FieldWeight scale the per-word keyword list and the
	// name/path/doc list against the keyword weight; nil takes the default, 0 turns the list off.
	WordWeight, FieldWeight *float64
	// RRFK is the reciprocal-rank constant; 0 means the default (20).
	RRFK int
	// GroupBy "file" returns one chunk per file; Dedupe drops chunks whose text repeats a higher hit.
	GroupBy string
	Dedupe  bool
	// Expand, when set, asks a chat model for extra keywords and a hypothetical
	// answer before searching; ExpandWeight scales those lists (0 means 0.5),
	// ExpandTimeout bounds the call (0 means 3s).
	Expand        embedder.Chat
	ExpandWeight  float64
	ExpandTimeout time.Duration
	// Root is the project root, used for git-based recency and changed-since filters.
	Root string

	expandOnce  sync.Once
	expandCache *expandCache

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
	// GroupBy overrides the searcher's grouping: "file" or "chunk"; empty keeps the default.
	GroupBy string
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
	// defaultWordWeight scales the per-word keyword list against the other lists.
	// Measured on this repo's eval set: 0.25 beat 0.1, 0.5 and 0.75 (recall@5 0.9 vs 0.8).
	defaultWordWeight = 0.25
)

// wordMatches runs a keyword lookup per query word and ranks chunks by how
// many words they contain, so "retry 429 embedder" favours the chunk that has
// all three. The whole-question keyword search rarely matches prose questions.
func (s *Searcher) wordMatches(ctx context.Context, words []string, pool int, workspace string) []parser.Chunk {
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
	chat, err := embedder.NewChat(cfg.Search.Expand)
	if err != nil {
		store.Close()
		return nil, nil, fmt.Errorf("search.expand: %w", err)
	}
	return &Searcher{Store: store, Emb: embedder.Cached(emb, EmbedCacheSize), Rerank: rr, Candidates: cfg.Reranker.Candidates,
		RerankTimeout:  cfg.Reranker.Timeout(),
		SemanticWeight: cfg.Search.SemanticWeight, KeywordWeight: cfg.Search.KeywordWeight,
		RecencyWeight: cfg.Search.RecencyWeight, RecencyDays: cfg.Search.RecencyDays,
		WordWeight: cfg.Search.WordWeight, FieldWeight: cfg.Search.FieldWeight, RRFK: cfg.Search.RRFK,
		GroupBy: cfg.Search.GroupBy, Dedupe: cfg.Search.Dedupe,
		Expand: chat, ExpandTimeout: cfg.Search.Expand.Timeout(),
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

// fuseScored merges ranked lists by weighted reciprocal rank, best first, with
// rank constant k. Ties break by path, then content, so the order is stable.
func fuseScored(k int, weights []float64, lists ...[]parser.Chunk) []scored {
	var order []string
	by := map[string]*scored{}
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
				item = &scored{chunk: ch}
				by[key] = item
				order = append(order, key)
			}
			item.score += w / float64(k+rank)
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
