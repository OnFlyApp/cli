package query

import (
	"context"
	"math"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

// The vector list weighs four times the lexical ones. A question that
// describes behavior shares few words with the code, and on the symbol-less
// suites the vectors found the file where the lexical lists did not (Hugo 0.29
// to 0.57, this repository 0.40 to 0.60 at recall@5); tiny declarations the
// embedder likes are moved back by demoteBoilerplate and the tiny-declaration rule.
const (
	focusLexical  = 0.5
	focusSemantic = 2.0
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
	ranked, g, err := s.focusRank(ctx, r)
	if err != nil {
		return nil, err
	}
	if s.Expander != nil && s.lowConfidence(ranked, g) {
		if kws, err := s.Expander.Expand(ctx, r.Query); err != nil {
			s.logger().Warn("query expansion", "err", err)
		} else if len(kws) > 0 {
			r2 := r
			r2.Query = strings.Join(kws, " ")
			r2.LexicalOnly = true // identifiers are matched, not embedded
			if more, _, err := s.focusRank(ctx, r2); err == nil {
				s.logger().Debug("query expanded", "query", r.Query, "keywords", kws)
				ranked = FuseWeighted([]float64{1, expandedWeight}, ranked, more)
			}
		}
	}
	fused := s.expandFileSummaries(ctx, r.Query, s.rerank(ctx, r.Query, ranked))
	if n := ClampLimit(r.Limit); len(fused) > n {
		fused = fused[:n]
	}
	return fused, nil
}

// focusRank is the ranking of onfly_focus before the reranker.
func (s *Searcher) focusRank(ctx context.Context, r Request) ([]parser.Chunk, *Gathered, error) {
	g, err := s.Gather(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	ranked := demoteTests(demoteBoilerplate(s.Fuse(g, FocusParams(s.Params())), g.Terms), g.Terms)
	// Cohesion rewards a file that holds the question's words anywhere in its
	// text. That is the evidence a lexical index has; next to a vector list it
	// favours big files that hold every common word over the file that is about
	// the question, so it is left out when vectors took part.
	if len(g.Semantic) == 0 {
		ranked = s.boostCohesion(ctx, ranked, g.Terms, g.Weights, g.Absent)
	}
	extras := s.pathCandidates(ctx, specificTerms(g.Terms, g.Weights), g.Terms, g.CodeOnly)
	return boostPathTokens(ranked, extras, g.Terms, g.Weights), g, nil
}

// lowConfidence says that none of the top three results is named for the
// question: the share of the question's weight that a result's name or path
// covers is under a half. A question that repeats the code's own words is not
// expanded, so the model is asked only where search is likely to miss.
func (s *Searcher) lowConfidence(ranked []parser.Chunk, g *Gathered) bool {
	var total float64
	for _, t := range g.Terms {
		total += weightOf(g.Weights, t)
	}
	if total <= 0 {
		return false
	}
	best := 0.0
	for i, c := range ranked {
		if i == 3 {
			break
		}
		have := splitWords(c.Metadata["name"] + " " + strings.TrimSuffix(c.FilePath, path.Ext(c.FilePath)))
		var got float64
		for _, t := range g.Terms {
			for _, w := range splitWords(t) {
				if countHitsOne(w, have) {
					got += weightOf(g.Weights, t)
					break
				}
			}
		}
		best = math.Max(best, got/total)
	}
	return best < lowConfidenceCoverage
}

const (
	lowConfidenceCoverage = 0.5
	// expandedWeight favours the list made from the model's identifiers: it is
	// only built when the first list looked weak.
	expandedWeight = 1
)

func countHitsOne(word string, have []string) bool {
	for _, h := range have {
		if sameWord(word, h) {
			return true
		}
	}
	return false
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
	"struct": true, "type": true, "constant": true,
	"property": true, "enum_case": true, "trait_use": true,
	// One-line declarations of what a file imports or which namespace it is in.
	"use": true, "namespace": true, "statement": true,
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
func (s *Searcher) pathCandidates(ctx context.Context, terms, all []string, codeOnly bool) []parser.Chunk {
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
	cover := coveringFiles(files, all, codeOnly)
	for _, f := range picked {
		if !slices.Contains(cover, f) {
			cover = append(cover, f)
		}
	}
	picked = cover
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

// coveringFiles returns the files whose path words (camelCase split, so
// StatementCycle/Command/EndCycleHandler.php holds statement, cycle, command,
// end and handler) cover most of the query's words, best first. Words that
// are common in the index still count: together they name the file.
func coveringFiles(files, terms []string, codeOnly bool) []string {
	var words []string
	for _, t := range terms {
		if len(t) >= 3 {
			words = append(words, strings.ToLower(t))
		}
	}
	need := (3*len(words) + 3) / 4
	if len(words) < 3 || need < 3 {
		return nil
	}
	type scored struct {
		f string
		n int
	}
	var hits []scored
	for _, f := range files {
		if isTestPath(f) || codeOnly && vectorstore.IsProse(f) {
			continue
		}
		have := splitWords(strings.TrimSuffix(f, filepath.Ext(f)))
		n := 0
		for _, w := range words {
			if countHitsOne(w, have) || countHitsOne(strings.TrimSuffix(w, "s"), have) {
				n++
			}
		}
		if n >= need {
			hits = append(hits, scored{f, n})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].n > hits[j].n })
	var out []string
	for i, h := range hits {
		if i == pathFiles {
			break
		}
		out = append(out, h.f)
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
// file of the path, and 0 when none does or the only ones are too common. When
// two or more terms each match a word of the path (StatementCycle/EndCycleHandler
// holds statement, cycle, end and handler), the share of the query's weight
// they cover counts too, so a path that names the whole question beats one
// that names part of it.
func pathWeight(file string, terms []string, weights map[string]float64) float64 {
	best := 0.0
	var total, got float64
	words := splitWords(strings.TrimSuffix(file, filepath.Ext(file)))
	hits := 0
	for _, t := range terms {
		w := weightOf(weights, t)
		if w < minTermWeight {
			continue
		}
		total += w
		if countHitsOne(strings.ToLower(t), words) {
			got += w
			hits++
		}
		if w > best && pathNamed(file, []string{t}) {
			best = w
		}
	}
	if hits >= 2 && total > 0 {
		best = math.Max(best, got/total)
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
