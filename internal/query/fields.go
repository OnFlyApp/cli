package query

import (
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/axitdev/onfly/internal/parser"
)

const (
	nameHit    = 3.0
	nameCover  = 2.0
	baseHit    = 2.0
	dirHit     = 1.0
	docHit     = 1.0
	docLines   = 3
	minStemLen = 4
)

// fieldMatches ranks candidates by how well their name, file name, directory and
// leading comment match the query words. weights, if given, scale each word by
// its rarity.
func fieldMatches(terms []string, cands []parser.Chunk, weights map[string]float64) []parser.Chunk {
	if len(terms) == 0 || len(cands) == 0 {
		return nil
	}
	var want []weighted
	for _, t := range terms {
		for _, w := range splitWords(t) {
			want = append(want, weighted{w, weightOf(weights, t)})
		}
	}
	type item struct {
		chunk parser.Chunk
		score float64
		pos   int
	}
	var items []item
	for i, c := range cands {
		sc := fieldScore(want, c)
		if sc > 0 {
			items = append(items, item{c, sc, i})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].pos < items[j].pos
	})
	out := make([]parser.Chunk, len(items))
	for i, it := range items {
		out[i] = it.chunk
	}
	return out
}

type weighted struct {
	word   string
	weight float64
}

func fieldScore(want []weighted, c parser.Chunk) float64 {
	var score float64
	if name := splitWords(c.Metadata["name"]); len(name) > 0 {
		hits, covered := countHits(want, name)
		score += nameHit * hits
		if covered == len(name) {
			score += nameCover
		}
	}
	dir, file := path.Split(c.FilePath)
	base := strings.TrimSuffix(file, path.Ext(file))
	score += baseHit * hitsIn(want, splitWords(base))
	score += dirHit * hitsIn(want, splitWords(dir))
	score += docHit * hitsIn(want, splitWords(c.Metadata["summary"]+" "+leadingComment(c.Content)))
	return score
}

// countHits sums the weights of the query words that appear in have, and
// counts how many words of have some query word matches.
func countHits(want []weighted, have []string) (float64, int) {
	var sum float64
	for _, w := range want {
		for _, h := range have {
			if sameWord(w.word, h) {
				sum += w.weight
				break
			}
		}
	}
	covered := 0
	for _, h := range have {
		for _, w := range want {
			if sameWord(w.word, h) {
				covered++
				break
			}
		}
	}
	return sum, covered
}

func hitsIn(want []weighted, have []string) float64 {
	sum, _ := countHits(want, have)
	return sum
}

func sameWord(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) < minStemLen || len(b) < minStemLen {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a) || stem(a) == stem(b)
}

func stem(w string) string {
	for _, suf := range []string{"ies", "ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			if suf == "ies" {
				return w[:len(w)-3] + "y"
			}
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

func splitWords(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 &&
			(unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

func leadingComment(content string) string {
	var b strings.Builder
	n := 0
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		t := strings.TrimLeft(l, "/#*- ")
		if t == l && n > 0 {
			break
		}
		if t != l || n == 0 {
			b.WriteString(t + " ")
			n++
		}
		if n >= docLines {
			break
		}
	}
	return b.String()
}
