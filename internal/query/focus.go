package query

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
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
	r.CodeOnly = true
	g, err := s.Gather(ctx, r)
	if err != nil {
		return nil, err
	}
	ranked := demoteTests(demoteBoilerplate(s.Fuse(g, FocusParams(s.Params())), g.Terms), g.Terms)
	ranked = s.boostCohesion(ctx, ranked, g.Terms, g.Weights, g.Absent)
	extras := s.pathCandidates(ctx, specificTerms(g.Terms, g.Weights), g.CodeOnly)
	fused := s.rerank(ctx, r.Query, boostPathTokens(ranked, extras, g.Terms, g.Weights))
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
func boostPathTokens(ranked, extras []parser.Chunk, terms []string, weights map[string]float64) []parser.Chunk {
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
		if w := pathWeight(c.FilePath, terms, weights); w > 0 {
			it.key *= 1 - (1-pathBoost)*w // a rarer word moves the chunk further
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
func (s *Searcher) pathCandidates(ctx context.Context, terms []string, codeOnly bool) []parser.Chunk {
	files, err := s.Store.ListFiles(ctx)
	if err != nil {
		return nil
	}
	var picked []string
	for _, f := range files {
		if pathNamed(f, terms) && !(codeOnly && vectorstore.IsProse(f)) {
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

// pathWeight is the highest weight among the terms that name a directory or
// file of the path, and 0 when none does or the only ones are too common.
func pathWeight(file string, terms []string, weights map[string]float64) float64 {
	best := 0.0
	for _, t := range terms {
		w := weightOf(weights, t)
		if w < minTermWeight || w <= best {
			continue
		}
		if pathNamed(file, []string{t}) {
			best = w
		}
	}
	return best
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

// docWords in a query say it is about documentation, so prose is searched too.
var docWords = map[string]bool{
	"doc": true, "docs": true, "documentation": true, "documented": true, "readme": true,
	"guide": true, "tutorial": true, "changelog": true, "markdown": true,
}

func asksForDocs(terms []string) bool {
	for _, t := range terms {
		if docWords[strings.ToLower(t)] {
			return true
		}
	}
	return false
}

const (
	cohesionPool   = 40 // ranked chunks whose files are checked
	cohesionFiles  = 20 // distinct files read for that
	cohesionWeight = 1.5
)

// boostCohesion favours the files that, taken whole, mention the most of the
// query's rare words. A question such as "table of contents headings" is
// answered by a file whose one function matches "table" and another "headings";
// chunk by chunk each scores a third, together they cover the question. Each
// chunk's score is 1/rank plus cohesionWeight times the share of the query's
// weight that its whole file covers.
func (s *Searcher) boostCohesion(ctx context.Context, ranked []parser.Chunk, terms []string, weights map[string]float64, absent map[string]bool) []parser.Chunk {
	// A word found nowhere in the index cannot be covered by any file, so it
	// stays out of the share a file is judged by.
	var present []string
	var total float64
	for _, t := range terms {
		if !absent[strings.ToLower(t)] {
			present = append(present, t)
			total += weightOf(weights, t)
		}
	}
	terms = present
	if total <= 0 || len(ranked) < 2 {
		return ranked
	}
	head := ranked
	if len(head) > cohesionPool {
		head = ranked[:cohesionPool]
	}
	var files []string
	seen := map[string]bool{}
	for _, c := range head {
		if !seen[c.FilePath] && len(files) < cohesionFiles {
			seen[c.FilePath] = true
			files = append(files, c.FilePath)
		}
	}
	rows, err := s.Store.ChunksByFiles(ctx, files)
	if err != nil {
		return ranked
	}
	text := map[string]string{}
	for _, f := range files {
		text[f] = strings.ToLower(f)
	}
	for _, r := range rows {
		text[r.Chunk.FilePath] += "\n" + strings.ToLower(r.Chunk.Content)
	}
	coverage := map[string]float64{}
	for f, body := range text {
		for _, t := range terms {
			if st := stem(strings.ToLower(t)); len(st) >= 3 && strings.Contains(body, st) {
				coverage[f] += weightOf(weights, t)
			}
		}
		coverage[f] /= total
	}
	type scored struct {
		c     parser.Chunk
		score float64
	}
	items := make([]scored, len(ranked))
	for i, c := range ranked {
		items[i] = scored{c, 1 / float64(i+1)}
		if i < cohesionPool {
			items[i].score += cohesionWeight * coverage[c.FilePath]
		}
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].score > items[b].score })
	out := make([]parser.Chunk, len(items))
	for i, it := range items {
		out[i] = it.c
	}
	return out
}
