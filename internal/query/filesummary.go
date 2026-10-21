package query

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
)

const (
	// defaultSummarySymbols is how many symbols of a file stand in for its summary:
	// more than one takes the places of other files' matches (Hugo 0.57 to 0.29 at 3).
	defaultSummarySymbols = 1
	// defaultSummaryWeight is the weight of a summary next to a symbol in the fusion. A
	// summary says only that the file is about the question; 1 lets the summaries
	// of wrong files outrank a right symbol, 0.5 loses what they add on the
	// monorepo.
	defaultSummaryWeight = 0.7
)

// expandFileSummaries replaces each file summary in the ranking by symbols of
// that file, at the summary's place: a method or function the ranking already
// holds, best first, else the largest one the file has. Code to read, not the
// class that holds it. A summary says that the file
// is what the question is about; the code to read is in its symbols, and a
// symbol that matched on its own is not listed twice.
func (s *Searcher) expandFileSummaries(ctx context.Context, query string, ranked []parser.Chunk) []parser.Chunk {
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
	most := orDefaultInt(s.SummarySymbols, defaultSummarySymbols)
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
			if taken == most {
				break
			}
			if isCallable(own) && !placed[ChunkKey(own)] {
				add(own)
				taken++
			}
		}
		if taken == most {
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
		scores := s.scoreAgainst(ctx, query, c.FilePath, fill)
		sort.SliceStable(fill, func(i, j int) bool { return scores[ChunkKey(fill[i])] > scores[ChunkKey(fill[j])] })
		for _, f := range fill {
			if taken == most {
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

// lexicalShare is how much the words of the query count next to the cosine of
// the vectors when the methods of one file are compared.
const lexicalShare = 0.5

// scoreAgainst rates each chunk of one file by the query: the cosine of its
// vector with the query's, plus the share of the query's words that its name
// (counted whole) or its text (counted half) holds. The method that stands for
// a file is the one that answers the question, not the first or the longest.
func (s *Searcher) scoreAgainst(ctx context.Context, query, file string, chunks []parser.Chunk) map[string]float64 {
	var qv []float32
	vecs := map[string][]float32{}
	if s.Emb != nil {
		if v, err := s.Emb.Embed(ctx, query); err == nil {
			if got, err := s.Store.VectorsByFile(ctx, file); err == nil {
				qv, vecs = v, got
			}
		}
	}
	terms := queryWords(query)
	out := make(map[string]float64, len(chunks))
	for _, c := range chunks {
		score := 0.0
		if v, ok := vecs[c.Content]; ok && len(qv) > 0 && len(v) == len(qv) {
			score += cosine(qv, v)
		}
		if len(terms) > 0 {
			name, text := splitWords(c.Metadata["name"]), strings.ToLower(c.Content)
			hit := 0.0
			for _, t := range terms {
				t = strings.ToLower(t)
				switch {
				case countHitsOne(t, name):
					hit++
				case strings.Contains(text, t):
					hit += 0.5
				}
			}
			score += lexicalShare * hit / float64(len(terms))
		}
		out[ChunkKey(c)] = score
	}
	return out
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
