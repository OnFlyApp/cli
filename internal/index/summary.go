package index

import (
	"context"
	"encoding/json"
	"fmt"
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
	if c.Metadata["name"] == "" && c.Type != "section" && c.Type != fileChunkType {
		return false
	}
	return c.Type == fileChunkType || len([]rune(c.Content)) >= summaryMinRunes
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
	var todo []int
	for i := range chunks {
		c := &chunks[i]
		if !summarizable(*c) {
			continue
		}
		if sum, ok := known[c.Content]; ok {
			c.Metadata["summary"] = sum
			continue
		}
		todo = append(todo, i)
	}
	batch := max(s.cfg.Indexer.Summaries.Batch, 1)
	failed := false
	set := func(i int, out string) {
		if sum := cleanSummary(out); sum != "" {
			chunks[i].Metadata["summary"] = sum
			delete(prev, chunks[i].Content)
		}
	}
	ask := func(prompt string) (string, bool) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := s.chat.Complete(cctx, prompt)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("summaries: model failed, skipping the rest of this file", "path", rel, "err", err)
			}
			failed = true
			return "", false
		}
		return out, true
	}
	for start := 0; start < len(todo) && !failed; start += batch {
		group := todo[start:min(start+batch, len(todo))]
		if len(group) > 1 {
			parts := make([]parser.Chunk, len(group))
			for j, i := range group {
				parts[j] = chunks[i]
			}
			out, ok := ask(batchPrompt(parts))
			if !ok {
				break
			}
			if got := parseBatch(out, len(group)); got != nil {
				for j, i := range group {
					set(i, got[j])
				}
				continue
			}
			// The model did not return one sentence per chunk: ask one by one.
		}
		for _, i := range group {
			if failed {
				break
			}
			if out, ok := ask(summaryPrompt(chunks[i])); ok {
				set(i, out)
			}
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
