package eval

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
)

// Outranker classes: what stood at rank 1 when the expected chunk did not.
const (
	SameFile    = "same file, other symbol" // the file was right, the symbol was not
	DocOverAPI  = "docs section over code"  // a Markdown section outranked the expected code
	CodeOverDoc = "code over docs section"  // the expected chunk is docs, code outranked it
	OtherCode   = "other file's code"
	Duplicate   = "same text elsewhere" // identical content in another file
)

// Miss is one case whose expected chunk was not first.
type Miss struct {
	Query  string
	Rank   int // 0 is a miss
	Want   []Expect
	Top    []parser.Chunk // the first few results
	Reason string         // class of the rank-1 hit
}

// Explain lists the cases whose expected chunk was not at rank 1, with the top
// results and the class of the hit that took rank 1. results maps a query to
// its results.
func Explain(cases []Case, results map[string][]parser.Chunk, k int, fileLevel bool) []Miss {
	var out []Miss
	for _, c := range cases {
		got := results[c.Query]
		r := Rank(c, got, k, fileLevel)
		if r == 1 {
			continue
		}
		m := Miss{Query: c.Query, Rank: r, Want: c.Expect, Top: got[:min(len(got), 3)]}
		if len(got) > 0 {
			m.Reason = classify(got[0], c, got, r)
		}
		out = append(out, m)
	}
	return out
}

func classify(top parser.Chunk, c Case, got []parser.Chunk, rank int) string {
	var expected *parser.Chunk
	if rank > 0 {
		expected = &got[rank-1]
	}
	for _, w := range c.Expect {
		if top.FilePath == w.File || strings.HasSuffix(top.FilePath, "/"+w.File) {
			return SameFile
		}
	}
	if expected != nil && top.Content == expected.Content {
		return Duplicate
	}
	switch {
	case top.Type == "section" && (expected == nil || expected.Type != "section"):
		return DocOverAPI
	case top.Type != "section" && expected != nil && expected.Type == "section":
		return CodeOverDoc
	}
	return OtherCode
}

// WriteExplain prints the class counts and each miss.
func WriteExplain(w io.Writer, misses []Miss, total int) {
	counts := map[string]int{}
	for _, m := range misses {
		counts[m.Reason]++
	}
	fmt.Fprintf(w, "\n%d of %d cases were not first. What took rank 1:\n", len(misses), total)
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
	for _, k := range keys {
		name := k
		if name == "" {
			name = "(no results)"
		}
		fmt.Fprintf(w, "  %4d  %s\n", counts[k], name)
	}
	for _, m := range misses {
		mark := fmt.Sprintf("rank %d", m.Rank)
		if m.Rank == 0 {
			mark = "miss"
		}
		fmt.Fprintf(w, "\n%s  %s\n", mark, m.Query)
		var want []string
		for _, e := range m.Want {
			s := e.File
			if e.Symbol != "" {
				s += "#" + e.Symbol
			}
			want = append(want, s)
		}
		fmt.Fprintf(w, "  want: %s\n", strings.Join(want, ", "))
		for i, c := range m.Top {
			s := c.FilePath
			if n := c.Metadata["name"]; n != "" {
				s += "#" + n
			}
			fmt.Fprintf(w, "  %d. %s [%s]\n", i+1, s, c.Type)
		}
	}
}
