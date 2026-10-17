package query

import (
	"context"
	"math"
	"strings"

	"github.com/axitdev/onfly/internal/vectorstore"
)

// minTermWeight is the weight under which a word is too common in the index to
// name a module: it neither boosts a file path nor adds a file by its name.
const minTermWeight = 0.4

// idfWeight is the rarity of a word that appears in df of n chunks, from 0
// (in every chunk) to 1 (in one or none): ln(1+(n-df+0.5)/(df+0.5)) relative to
// its value at df=1. Without statistics every word weighs the same.
func idfWeight(df, n int) float64 {
	if n <= 0 {
		return 1
	}
	idf := math.Log(1 + (float64(n-df)+0.5)/(float64(df)+0.5))
	most := math.Log(1 + (float64(n)-0.5)/1.5)
	if most <= 0 {
		return 1
	}
	return math.Max(0, math.Min(1, idf/most))
}

// termWeights weighs each query word by how rare it is in the index, so that
// "content" in a site generator counts for little and "toc" for a lot. The key
// is the lower-cased word. A store that cannot count gives every word 1.
func (s *Searcher) termWeights(ctx context.Context, terms []string, workspace string) map[string]float64 {
	out := make(map[string]float64, len(terms))
	stats, ok := s.Store.(vectorstore.TermStats)
	for _, t := range terms {
		lt := strings.ToLower(t)
		out[lt] = 1
		if !ok || len(lt) < 3 {
			continue
		}
		if df, n, err := stats.DocFreq(ctx, lt, workspace); err == nil {
			out[lt] = idfWeight(df, n)
		}
	}
	return out
}

func weightOf(weights map[string]float64, term string) float64 {
	if w, ok := weights[strings.ToLower(term)]; ok {
		return w
	}
	return 1
}

// specificTerms keeps the words rare enough to name a module.
func specificTerms(terms []string, weights map[string]float64) []string {
	var out []string
	for _, t := range terms {
		if weightOf(weights, t) >= minTermWeight {
			out = append(out, t)
		}
	}
	return out
}
