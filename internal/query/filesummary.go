package query

import (
	"context"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
)

const (
	// summarySymbols is how many symbols of a file stand in for its summary: more
	// than one takes the places of other files' matches (Hugo 0.57 to 0.29 at 3).
	summarySymbols = 1
	// summaryWeight is the weight of a summary next to a symbol in the fusion. A
	// summary says only that the file is about the question; 1 lets the summaries
	// of wrong files outrank a right symbol, 0.5 loses what they add on the
	// monorepo.
	summaryWeight = 0.7
)

// expandFileSummaries replaces each file summary in the ranking by symbols of
// that file, at the summary's place: a method or function the ranking already
// holds, best first, else the largest one the file has. Code to read, not the
// class that holds it. A summary says that the file
// is what the question is about; the code to read is in its symbols, and a
// symbol that matched on its own is not listed twice.
func (s *Searcher) expandFileSummaries(ctx context.Context, ranked []parser.Chunk) []parser.Chunk {
	has := false
	for _, c := range ranked {
		if c.Type == parser.FileSummaryType {
			has = true
			break
		}
	}
	if !has {
		return ranked
	}
	byFile := map[string][]parser.Chunk{}
	for _, c := range ranked {
		if c.Type != parser.FileSummaryType {
			byFile[c.FilePath] = append(byFile[c.FilePath], c)
		}
	}
	placed := map[string]bool{}
	out := make([]parser.Chunk, 0, len(ranked))
	add := func(c parser.Chunk) {
		if k := ChunkKey(c); !placed[k] {
			placed[k] = true
			out = append(out, c)
		}
	}
	for _, c := range ranked {
		if c.Type != parser.FileSummaryType {
			add(c)
			continue
		}
		taken := 0
		for _, own := range byFile[c.FilePath] {
			if taken == summarySymbols {
				break
			}
			if isCallable(own) && !placed[ChunkKey(own)] {
				add(own)
				taken++
			}
		}
		if taken == summarySymbols {
			continue
		}
		rows, err := s.Store.ChunksByFiles(ctx, []string{c.FilePath})
		if err != nil {
			continue
		}
		var fill []parser.Chunk
		for _, r := range rows {
			if isCallable(r.Chunk) && r.Chunk.Metadata["name"] != "" && !placed[ChunkKey(r.Chunk)] {
				fill = append(fill, r.Chunk)
			}
		}
		sort.SliceStable(fill, func(i, j int) bool { return len(fill[i].Content) > len(fill[j].Content) })
		for _, f := range fill {
			if taken == summarySymbols {
				break
			}
			add(f)
			taken++
		}
	}
	return out
}

func isCallable(c parser.Chunk) bool {
	t := strings.ToLower(c.Type)
	return t == "function" || t == "method"
}
