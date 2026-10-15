package index

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/axitdev/onfly/internal/parser"
)

const (
	summaryMinRunes     = 60
	summaryMaxInput     = 3000
	summaryMaxRunes     = 300
	defaultSummaryLimit = 60 * time.Second
)

// summarizable reports whether a chunk is worth a generated description: a
// named symbol or a docs section with enough text to say something about.
func summarizable(c parser.Chunk) bool {
	if c.Metadata["name"] == "" && c.Type != "section" && c.Type != fileChunkType {
		return false
	}
	return c.Type == fileChunkType || len([]rune(c.Content)) >= summaryMinRunes
}

// generated is what the model wrote for one chunk.
type generated struct {
	Summary   string
	Questions []string
}

func (g generated) empty() bool { return g.Summary == "" && len(g.Questions) == 0 }

// apply stores g on the chunk's metadata.
func (g generated) apply(c *parser.Chunk) {
	if g.Summary != "" {
		c.Metadata["summary"] = g.Summary
	}
	if len(g.Questions) > 0 {
		c.Metadata["questions"] = strings.Join(g.Questions, " | ")
	}
}

// addSummaries sets Metadata["summary"] (and ["questions"]) on the chunks of
// one file. A chunk is answered, in order, from what the file already stored,
// from the summary cache, and only then from the model, so a reindex or an
// embedder change calls the model only for chunks it has never seen. It
// returns prev without the vectors of chunks that gained text, so those are
// embedded again. A model failure stops summarising this file and leaves the
// rest as they are: indexing never fails because the summary model is down.
func (s *Session) addSummaries(ctx context.Context, rel string, chunks []parser.Chunk, prev map[string][]float32) map[string][]float32 {
	wantQ := s.cfg.Indexer.Summaries.Questions
	stored := map[string]generated{}
	if old, err := s.store.ChunksByFiles(ctx, []string{rel}); err != nil {
		s.logger.Warn("summaries: stored chunks", "path", rel, "err", err)
	} else {
		for _, o := range old {
			g := generated{Summary: o.Chunk.Metadata["summary"]}
			if q := o.Chunk.Metadata["questions"]; q != "" {
				g.Questions = strings.Split(q, " | ")
			}
			if g.Summary != "" && (wantQ == 0 || len(g.Questions) > 0) {
				stored[o.Chunk.Content] = g
			}
		}
	}
	timeout := s.cfg.Indexer.Summaries.Timeout()
	if timeout <= 0 {
		timeout = defaultSummaryLimit
	}
	model := s.cfg.Indexer.Summaries.Model
	var ctxOf func(i int) string
	if s.cfg.Indexer.Summaries.Context {
		ctxOf = contextFor(chunks)
	}
	ctxAt := func(i int) string {
		if ctxOf == nil {
			return ""
		}
		return ctxOf(i)
	}
	key := func(i int) string { return summaryKey(model, wantQ, chunks[i], ctxAt(i)) }
	var todo []int
	for i := range chunks {
		c := &chunks[i]
		if !summarizable(*c) {
			continue
		}
		if g, ok := stored[c.Content]; ok {
			g.apply(c)
			s.sums.put(key(i), g) // an index made before the cache existed seeds it
			continue
		}
		if g, ok := s.sums.get(key(i)); ok {
			g.apply(c)
			delete(prev, c.Content)
			continue
		}
		todo = append(todo, i)
	}
	batch := max(s.cfg.Indexer.Summaries.Batch, 1)
	if wantQ > 0 {
		batch = 1 // questions need the model's full attention on one chunk
	}
	var failed atomic.Bool
	ask := func(prompt string) (string, bool) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := s.chat.Complete(cctx, prompt)
		if err != nil {
			if ctx.Err() == nil && failed.CompareAndSwap(false, true) {
				s.logger.Warn("summaries: model failed, skipping the rest of this file", "path", rel, "err", err)
			}
			failed.Store(true)
			return "", false
		}
		return out, true
	}
	type answer struct {
		i int
		g generated
	}
	// job asks the model about one group of chunks and returns what it wrote.
	job := func(group []int) []answer {
		if failed.Load() {
			return nil
		}
		var res []answer
		if len(group) > 1 {
			parts := make([]parser.Chunk, len(group))
			for j, i := range group {
				parts[j] = chunks[i]
			}
			out, ok := ask(batchPrompt(parts))
			if !ok {
				return nil
			}
			if got := parseBatch(out, len(group)); got != nil {
				for j, i := range group {
					res = append(res, answer{i, generated{Summary: cleanSummary(got[j])}})
				}
				return res
			}
			// The model did not return one sentence per chunk: ask one by one.
		}
		for _, i := range group {
			if failed.Load() {
				break
			}
			if out, ok := ask(summaryPrompt(chunks[i], wantQ, ctxAt(i))); ok {
				res = append(res, answer{i, parseGenerated(out, wantQ)})
			}
		}
		return res
	}
	var groups [][]int
	for start := 0; start < len(todo); start += batch {
		groups = append(groups, todo[start:min(start+batch, len(todo))])
	}
	workers := max(s.cfg.Indexer.Summaries.Concurrency, 1)
	results := make(chan []answer)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	go func() {
		for _, g := range groups {
			sem <- struct{}{}
			wg.Add(1)
			go func(g []int) {
				defer wg.Done()
				defer func() { <-sem }()
				results <- job(g)
			}(g)
		}
		wg.Wait()
		close(results)
	}()
	// Answers are applied here, one at a time: prev and the cache are shared.
	for res := range results {
		for _, a := range res {
			if a.g.empty() {
				continue
			}
			a.g.apply(&chunks[a.i])
			s.sums.put(key(a.i), a.g)
			delete(prev, chunks[a.i].Content)
		}
	}
	return prev
}

// parseGenerated reads one answer: JSON with "summary" and "questions" when
// questions were asked for, otherwise (or when the model ignored the format)
// the first line of the text is the summary.
func parseGenerated(out string, wantQ int) generated {
	if wantQ > 0 {
		text := out
		if i := strings.Index(text, "{"); i >= 0 {
			if j := strings.LastIndex(text, "}"); j > i {
				text = text[i : j+1]
			}
		}
		var obj struct {
			Summary   string   `json:"summary"`
			Questions []string `json:"questions"`
		}
		if json.Unmarshal([]byte(text), &obj) == nil && strings.TrimSpace(obj.Summary) != "" {
			g := generated{Summary: cleanSummary(obj.Summary)}
			for _, q := range obj.Questions {
				if q = strings.TrimSpace(q); q != "" && !strings.Contains(q, " | ") && len(g.Questions) < wantQ {
					g.Questions = append(g.Questions, q)
				}
			}
			return g
		}
	}
	return generated{Summary: cleanSummary(out)}
}

func summaryPrompt(c parser.Chunk, questions int, context string) string {
	code := c.Content
	if r := []rune(code); len(r) > summaryMaxInput {
		code = string(r[:summaryMaxInput])
	}
	var b strings.Builder
	b.WriteString("Describe in one plain sentence (at most 30 words) what this code does or documents, ")
	b.WriteString("in everyday words a developer might type into a search box. Do not quote the code or repeat its name. ")
	if questions > 0 {
		fmt.Fprintf(&b, "Then write %d different short questions a developer might type into a search box that this code answers, "+
			"in their own words, without the code's identifiers. ", questions)
		b.WriteString("Answer with JSON only: {\"summary\": \"...\", \"questions\": [\"...\"]}\n\n")
	} else {
		b.WriteString("Answer with the sentence only.\n\n")
	}
	b.WriteString("File: " + c.FilePath + "\n")
	if n := c.Metadata["name"]; n != "" {
		b.WriteString("Name: " + n + "\n")
	}
	if context != "" {
		b.WriteString(context)
	}
	b.WriteString("\n" + code)
	return b.String()
}

// cleanSummary keeps the first non-empty line, without quotes or a leading label.
func cleanSummary(out string) string {
	for _, l := range strings.Split(out, "\n") {
		l = strings.Trim(strings.TrimSpace(l), "\"'`")
		l = strings.TrimPrefix(l, "Summary:")
		l = strings.Trim(strings.TrimSpace(l), "\"'`")
		if l == "" {
			continue
		}
		if r := []rune(l); len(r) > summaryMaxRunes {
			l = string(r[:summaryMaxRunes])
		}
		return l
	}
	return ""
}

const batchChunkRunes = 1200

// batchPrompt asks for one sentence per numbered chunk, as a JSON array.
func batchPrompt(parts []parser.Chunk) string {
	var b strings.Builder
	b.WriteString("Describe each numbered piece of code in one plain sentence (at most 30 words), ")
	b.WriteString("in everyday words a developer might type into a search box. Do not quote the code or repeat its name. ")
	b.WriteString("Answer with JSON only: {\"summaries\": [\"sentence for 1\", \"sentence for 2\", ...]} with exactly one sentence per piece, in order.\n\n")
	for i, c := range parts {
		code := c.Content
		if r := []rune(code); len(r) > batchChunkRunes {
			code = string(r[:batchChunkRunes])
		}
		fmt.Fprintf(&b, "[%d] File: %s\n", i+1, c.FilePath)
		if n := c.Metadata["name"]; n != "" {
			b.WriteString("Name: " + n + "\n")
		}
		b.WriteString(code + "\n\n")
	}
	return b.String()
}

// parseBatch returns n sentences from a batch answer, or nil when the answer
// does not hold exactly n non-empty sentences.
func parseBatch(out string, n int) []string {
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			out = out[i : j+1]
		}
	}
	var obj struct {
		Summaries []string `json:"summaries"`
	}
	if json.Unmarshal([]byte(out), &obj) != nil || len(obj.Summaries) != n {
		return nil
	}
	for _, s := range obj.Summaries {
		if strings.TrimSpace(s) == "" {
			return nil
		}
	}
	return obj.Summaries
}

const (
	fileChunkType   = "file"
	fileChunkSymbol = 40
	neighbourCount  = 8
	neighbourSpan   = 6
)

// fileChunk is one extra chunk per file: its path and the names it defines.
// With a summary it answers questions about what a file or package is for.
// It returns false for a file that defines nothing nameable.
func fileChunk(rel string, chunks []parser.Chunk) (parser.Chunk, bool) {
	var names []string
	seen := map[string]bool{}
	for _, c := range chunks {
		n := c.Metadata["name"]
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
		if len(names) == fileChunkSymbol {
			break
		}
	}
	if len(names) == 0 {
		return parser.Chunk{}, false
	}
	return parser.Chunk{
		FilePath: rel,
		Type:     fileChunkType,
		Content:  "file: " + rel + "\ndefines: " + strings.Join(names, ", "),
		Metadata: map[string]string{},
	}, true
}

// addNeighbours records, on every named chunk, the names of the symbols that
// sit next to it in the file, so the embedded text carries its surroundings.
func addNeighbours(chunks []parser.Chunk) {
	for i := range chunks {
		own := chunks[i].Metadata["name"]
		if own == "" {
			continue
		}
		var near []string
		seen := map[string]bool{own: true}
		for d := 1; d <= neighbourSpan && len(near) < neighbourCount; d++ {
			for _, j := range []int{i - d, i + d} {
				if j < 0 || j >= len(chunks) || len(near) >= neighbourCount {
					continue
				}
				if n := chunks[j].Metadata["name"]; n != "" && !seen[n] {
					seen[n] = true
					near = append(near, n)
				}
			}
		}
		if len(near) > 0 {
			chunks[i].Metadata["neighbours"] = strings.Join(near, ", ")
		}
	}
}

const (
	contextCalls   = 8
	contextCallers = 6
	contextNearby  = 6
	contextSpan    = 3
)

// contextFor returns, for each chunk of one file, a short block that says
// where the chunk sits: which symbols of the same file it calls and is called
// by, and which symbols are next to it. Only the file itself is used, so the
// text for a chunk does not change when other files are indexed, and the
// summary cache stays valid. The model gets it to describe a chunk's role, not
// only its body.
func contextFor(chunks []parser.Chunk) func(i int) string {
	defined := map[string]bool{}
	for _, c := range chunks {
		if n := c.Metadata["name"]; n != "" {
			defined[n] = true
		}
	}
	callers := map[string][]string{}
	for _, c := range chunks {
		self := c.Metadata["name"]
		if self == "" {
			continue
		}
		seen := map[string]bool{}
		for _, callee := range c.Outbound {
			callee = callee[strings.LastIndex(callee, ".")+1:]
			if callee == self || !defined[callee] || seen[callee] {
				continue
			}
			seen[callee] = true
			callers[callee] = append(callers[callee], self)
		}
	}
	return func(i int) string {
		c := chunks[i]
		self := c.Metadata["name"]
		var b strings.Builder
		var calls []string
		seen := map[string]bool{self: true}
		for _, callee := range c.Outbound {
			callee = callee[strings.LastIndex(callee, ".")+1:]
			if defined[callee] && !seen[callee] && len(calls) < contextCalls {
				seen[callee] = true
				calls = append(calls, callee)
			}
		}
		if len(calls) > 0 {
			b.WriteString("Calls (same file): " + strings.Join(calls, ", ") + "\n")
		}
		by := dedupeKeep(callers[self], self, contextCallers)
		if len(by) > 0 {
			b.WriteString("Called by (same file): " + strings.Join(by, ", ") + "\n")
		}
		var near []string
		nseen := map[string]bool{self: true}
		for d := 1; d <= contextSpan && len(near) < contextNearby; d++ {
			for _, j := range []int{i - d, i + d} {
				if j < 0 || j >= len(chunks) || len(near) >= contextNearby {
					continue
				}
				if n := chunks[j].Metadata["name"]; n != "" && !nseen[n] {
					nseen[n] = true
					near = append(near, n)
				}
			}
		}
		if len(near) > 0 {
			b.WriteString("Nearby: " + strings.Join(near, ", ") + "\n")
		}
		return b.String()
	}
}

func dedupeKeep(in []string, skip string, limit int) []string {
	var out []string
	seen := map[string]bool{skip: true}
	for _, s := range in {
		if !seen[s] && len(out) < limit {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
