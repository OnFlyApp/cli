// Package query runs one search: embed, semantic, keyword, reciprocal-rank fusion.
package query

import (
	"context"
	"io"
	"log/slog"
	"sort"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

const (
	DefaultLimit = 10
	MaxLimit     = 50
	keywordPool  = 20
	rrfK         = 60
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
}

func (s *Searcher) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Logger
}

func (s *Searcher) Search(ctx context.Context, q string, limit int, workspace string) ([]parser.Chunk, error) {
	vec, err := s.Emb.Embed(ctx, q)
	if err != nil {
		return nil, err
	}
	pool := CandidatePool(limit)
	semantic, err := s.Store.SearchSemantic(ctx, vec, pool, workspace)
	if err != nil {
		return nil, err
	}
	keyword, err := s.Store.SearchKeyword(ctx, q, pool, workspace)
	if err != nil {
		s.logger().Warn("search keyword", "err", err)
		keyword = nil
	}
	fused := Fuse(semantic, keyword)
	if n := ClampLimit(limit); len(fused) > n {
		fused = fused[:n]
	}
	return fused, nil
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
	return &Searcher{Store: store, Emb: emb}, store.Close, nil
}

// Fuse merges ranked lists by reciprocal rank. A chunk in several lists
// appears once and outranks a chunk that is first in only one list.
func Fuse(lists ...[]parser.Chunk) []parser.Chunk {
	type acc struct {
		chunk parser.Chunk
		score float64
	}
	var order []string
	by := map[string]*acc{}
	for _, list := range lists {
		seen := map[string]bool{}
		rank := 0
		for _, ch := range list {
			key := chunkKey(ch)
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
			item.score += 1 / float64(rrfK+rank)
		}
	}
	out := make([]parser.Chunk, 0, len(order))
	for _, key := range order {
		out = append(out, by[key].chunk)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := by[chunkKey(out[i])].score, by[chunkKey(out[j])].score
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

// chunkKey identifies a chunk across result lists. Path, type, name, part,
// first line and content together are unique per row.
func chunkKey(c parser.Chunk) string {
	m := c.Metadata
	return c.FilePath + "\x00" + c.Type + "\x00" + m["name"] + "\x00" + m["part"] + "\x00" + m["start_line"] + "\x00" + c.Content
}
