package index

import (
	"context"
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
	Summary string
}

func (g generated) empty() bool { return g.Summary == "" }

// apply stores g on the chunk's metadata.
func (g generated) apply(c *parser.Chunk) {
	if g.Summary != "" {
		c.Metadata["summary"] = g.Summary
	}
}

// addSummaries sets Metadata["summary"] on the chunks of
// one file. A chunk is answered, in order, from what the file already stored,
// from the summary cache, and only then from the model, so a reindex or an
// embedder change calls the model only for chunks it has never seen. It
// returns prev without the vectors of chunks that gained text, so those are
// embedded again. A model failure stops summarising this file and leaves the
// rest as they are: indexing never fails because the summary model is down.
func (s *Session) addSummaries(ctx context.Context, rel string, chunks []parser.Chunk, prev map[string][]float32) map[string][]float32 {
	stored := map[string]generated{}
	if old, err := s.store.ChunksByFiles(ctx, []string{rel}); err != nil {
		s.logger.Warn("summaries: stored chunks", "path", rel, "err", err)
	} else {
		for _, o := range old {
			if sum := o.Chunk.Metadata["summary"]; sum != "" {
				stored[o.Chunk.Content] = generated{Summary: sum}
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
	key := func(i int) string { return summaryKey(model, chunks[i], ctxAt(i)) }
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
	// job asks the model about one chunk and returns what it wrote.
	job := func(i int) []answer {
		if failed.Load() {
			return nil
		}
		if out, ok := ask(summaryPrompt(chunks[i], ctxAt(i))); ok {
			return []answer{{i, generated{Summary: cleanSummary(out)}}}
		}
		return nil
	}
	workers := max(s.cfg.Indexer.Summaries.Concurrency, 1)
	results := make(chan []answer)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	go func() {
		for _, i := range todo {
			sem <- struct{}{}
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				results <- job(i)
			}(i)
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

func summaryPrompt(c parser.Chunk, context string) string {
	code := c.Content
	if r := []rune(code); len(r) > summaryMaxInput {
		code = string(r[:summaryMaxInput])
	}
	var b strings.Builder
	b.WriteString("Describe in one plain sentence (at most 30 words) what this code does or documents, ")
	b.WriteString("in everyday words a developer might type into a search box. Do not quote the code or repeat its name. ")
	b.WriteString("Answer with the sentence only.\n\n")
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

const (
	fileChunkType   = "file"
	fileChunkSymbol = 40
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
