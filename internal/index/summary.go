package index

import (
	"context"
	"strings"
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
	if c.Metadata["name"] == "" && c.Type != "section" {
		return false
	}
	return len([]rune(c.Content)) >= summaryMinRunes
}

// addSummaries sets Metadata["summary"] on the chunks of one file. A chunk
// whose content the file already had keeps its stored summary, so a reindex
// calls the model only for new or changed chunks. It returns prev without the
// vectors of chunks that gained a new summary, so those are embedded again.
// A model failure stops summarising this file and leaves the rest as they are:
// indexing never fails because the summary model is down.
func (s *Session) addSummaries(ctx context.Context, rel string, chunks []parser.Chunk, prev map[string][]float32) map[string][]float32 {
	known := map[string]string{}
	if old, err := s.store.ChunksByFiles(ctx, []string{rel}); err != nil {
		s.logger.Warn("summaries: stored chunks", "path", rel, "err", err)
	} else {
		for _, o := range old {
			if sum := o.Chunk.Metadata["summary"]; sum != "" {
				known[o.Chunk.Content] = sum
			}
		}
	}
	timeout := s.cfg.Indexer.Summaries.Timeout()
	if timeout <= 0 {
		timeout = defaultSummaryLimit
	}
	failed := false
	for i := range chunks {
		c := &chunks[i]
		if !summarizable(*c) {
			continue
		}
		if sum, ok := known[c.Content]; ok {
			c.Metadata["summary"] = sum
			continue
		}
		if failed {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		out, err := s.chat.Complete(cctx, summaryPrompt(*c))
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("summaries: model failed, skipping the rest of this file", "path", rel, "err", err)
			}
			failed = true
			continue
		}
		if sum := cleanSummary(out); sum != "" {
			c.Metadata["summary"] = sum
			delete(prev, c.Content)
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
