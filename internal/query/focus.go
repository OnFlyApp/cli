package query

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
)

// Lexical lists (keyword, per-word, symbol and path) get 0.65 of the weight and
// semantic ones 0.35. A question about "skeleton body collapse" names things;
// the embedder alone puts small unrelated types ahead of the code that does it.
const (
	focusLexical  = 0.65
	focusSemantic = 0.35
	tinyLines     = 4
	pathBoost     = 0.5 // a chunk in a file the query names moves to half its rank position
)

func FocusParams(p Params) Params {
	p.Semantic, p.Keyword, p.Word, p.Field = focusSemantic, focusLexical, 1, 1
	if p.Alt > 0 {
		p.Alt = focusSemantic
	}
	return p
}

// RunFocus is Run with FocusParams, and with tiny declarations that the query
// does not name moved behind everything else.
func (s *Searcher) RunFocus(ctx context.Context, r Request) ([]parser.Chunk, error) {
	g, err := s.Gather(ctx, r)
	if err != nil {
		return nil, err
	}
	fused := s.rerank(ctx, r.Query, boostPathTokens(demoteTests(demoteBoilerplate(s.Fuse(g, FocusParams(s.Params())), g.Terms), g.Terms), s.pathCandidates(ctx, g.Terms), g.Terms))
	if n := ClampLimit(r.Limit); len(fused) > n {
		fused = fused[:n]
	}
	return fused, nil
}

// demoteTests moves chunks of test files behind the rest, unless the query
// asks about tests: a test repeats the words of the code it checks and would
// otherwise take the places of the implementation.
func demoteTests(ranked []parser.Chunk, terms []string) []parser.Chunk {
	for _, t := range terms {
		if strings.Contains(strings.ToLower(t), "test") || strings.EqualFold(t, "spec") {
			return ranked
		}
	}
	out := make([]parser.Chunk, 0, len(ranked))
	var tests []parser.Chunk
	for _, c := range ranked {
		if isTestPath(c.FilePath) {
			tests = append(tests, c)
		} else {
			out = append(out, c)
		}
	}
	return append(out, tests...)
}

func isTestPath(p string) bool {
	p = strings.ToLower(p)
	base := filepath.Base(p)
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.HasPrefix(base, "test_") || strings.Contains(p, "/tests/") || strings.Contains(p, "/__tests__/") || strings.HasPrefix(p, "tests/")
}

var boilerplateTypes = map[string]bool{
	"struct": true, "type": true, "interface": true, "constant": true,
	"property": true, "enum_case": true, "trait_use": true, "class": true,
}

func demoteBoilerplate(ranked []parser.Chunk, terms []string) []parser.Chunk {
	out := make([]parser.Chunk, 0, len(ranked))
	var tiny []parser.Chunk
	for _, c := range ranked {
		if boilerplateTypes[strings.ToLower(c.Type)] && strings.Count(strings.TrimSpace(c.Content), "\n") < tinyLines && !namedBy(c, terms) {
			tiny = append(tiny, c)
			continue
		}
		out = append(out, c)
	}
	return append(out, tiny...)
}

func namedBy(c parser.Chunk, terms []string) bool {
	name := strings.ToLower(c.Metadata["name"])
	if name == "" {
		return false
	}
	for _, t := range terms {
		if t = strings.ToLower(t); len(t) >= 3 && strings.Contains(name, t) {
			return true
		}
	}
	return false
}

const (
	pathFiles      = 3   // files whose name the query contains
	pathPerFile    = 6   // symbols taken from each
	pathExtraStart = 1.5 // rank position where symbols found only by path begin
)

// boostPathTokens favours the symbols of a module the query names. A term that
// equals a directory or file name in the chunk's path ("skeleton" in
// internal/skeleton/skeleton.go) halves the chunk's rank position, and the
// named symbols of such a file that no list returned are slotted in right after the
// strongest matches, so cohesive symbols outrank a single-word match in
// an unrelated package.
func boostPathTokens(ranked, extras []parser.Chunk, terms []string) []parser.Chunk {
	type keyed struct {
		c   parser.Chunk
		key float64
	}
	items := make([]keyed, 0, len(ranked)+len(extras))
	have := map[string]bool{}
	moved := false
	for i, c := range ranked {
		it := keyed{c, float64(i)}
		have[ChunkKey(c)] = true
		if pathNamed(c.FilePath, terms) {
			it.key *= pathBoost
			moved = true
		}
		items = append(items, it)
	}
	j := 0
	for _, c := range extras {
		if have[ChunkKey(c)] {
			continue
		}
		items = append(items, keyed{c, pathExtraStart + 0.01*float64(j)})
		j++
		moved = true
	}
	if !moved {
		return ranked
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].key < items[b].key })
	out := make([]parser.Chunk, len(items))
	for i, it := range items {
		out[i] = it.c
	}
	return out
}

// pathCandidates returns named symbols of the few files whose name a query term
// equals, in file order. The keyword lists only read chunk text, so a file that
// the query names but whose code never says the word is otherwise invisible.
func (s *Searcher) pathCandidates(ctx context.Context, terms []string) []parser.Chunk {
	files, err := s.Store.ListFiles(ctx)
	if err != nil {
		return nil
	}
	var picked []string
	for _, f := range files {
		if pathNamed(f, terms) {
			picked = append(picked, f)
		}
	}
	sort.Slice(picked, func(i, j int) bool { return baseNamed(picked[i], terms) && !baseNamed(picked[j], terms) })
	if len(picked) > pathFiles {
		picked = picked[:pathFiles]
	}
	if len(picked) == 0 {
		return nil
	}
	rows, err := s.Store.ChunksByFiles(ctx, picked)
	if err != nil {
		return nil
	}
	var out []parser.Chunk
	per := map[string]int{}
	for _, r := range rows {
		c := r.Chunk
		if c.Metadata["name"] == "" || per[c.FilePath] >= pathPerFile {
			continue
		}
		if boilerplateTypes[strings.ToLower(c.Type)] && strings.Count(strings.TrimSpace(c.Content), "\n") < tinyLines {
			continue
		}
		per[c.FilePath]++
		out = append(out, c)
	}
	return out
}

func baseNamed(file string, terms []string) bool {
	base := strings.ToLower(filepath.Base(file))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	for _, t := range terms {
		if t = strings.ToLower(t); len(t) >= 3 && base == t {
			return true
		}
	}
	return false
}

func pathNamed(file string, terms []string) bool {
	for _, seg := range strings.Split(strings.ToLower(file), "/") {
		seg = strings.TrimSuffix(seg, filepath.Ext(seg))
		for _, t := range terms {
			if t = strings.ToLower(t); len(t) >= 3 && seg == t {
				return true
			}
		}
	}
	return false
}
