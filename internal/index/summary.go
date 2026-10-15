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

func summarizable(c parser.Chunk) bool {
	if c.Metadata["name"] == "" && c.Type != "section" {
		return false
	}
	return len([]rune(c.Content)) >= summaryMinRunes
}

type generated struct {
	Summary string
}

func (g generated) empty() bool { return g.Summary == "" }

func (g generated) apply(c *parser.Chunk) {
	if g.Summary != "" {
		c.Metadata["summary"] = g.Summary
	}
}

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
	key := func(i int) string { return summaryKey(model, chunks[i]) }
	var todo []int
	for i := range chunks {
		c := &chunks[i]
		if !summarizable(*c) {
			continue
		}
		if g, ok := stored[c.Content]; ok {
			g.apply(c)
			s.sums.put(key(i), g)
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
	job := func(i int) []answer {
		if failed.Load() {
			return nil
		}
		if out, ok := ask(summaryPrompt(chunks[i])); ok {
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

func summaryPrompt(c parser.Chunk) string {
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
	b.WriteString("\n" + code)
	return b.String()
}

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
