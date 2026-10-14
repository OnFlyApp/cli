package parser

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
)

// TextParser is the built-in fallback for files with no language parser:
// markdown by heading, everything else by paragraph runs.
type TextParser struct{}

const blockLines = 30

func (TextParser) Close() error { return nil }

func (TextParser) Parse(_ context.Context, filePath string, content []byte) ([]Chunk, error) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return []Chunk{}, nil
	}
	base := filepath.Base(filePath)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	switch strings.ToLower(filepath.Ext(base)) {
	case ".md", ".markdown", ".mdx":
		return markdownSections(base, lines), nil
	}
	return paragraphBlocks(base, lines), nil
}

func textChunk(typ, name string, lines []string, start int) Chunk {
	end := start + len(lines) - 1
	return Chunk{
		Type:    typ,
		Content: strings.Join(lines, "\n"),
		Metadata: map[string]string{
			"name":       name,
			"start_line": strconv.Itoa(start),
			"end_line":   strconv.Itoa(end),
		},
	}
}

func blank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

func markdownSections(base string, lines []string) []Chunk {
	var out []Chunk
	name, start, inFence := base, 1, false
	flush := func(end int) {
		part := lines[start-1 : end]
		if !blank(part) {
			out = append(out, textChunk("section", name, part, start))
		}
	}
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(line, "#") {
			continue
		}
		hashes := len(line) - len(strings.TrimLeft(line, "#"))
		heading := strings.TrimSpace(line[hashes:])
		if hashes > 6 || heading == "" || line[hashes] != ' ' {
			continue
		}
		if i > 0 {
			flush(i)
		}
		name, start = base+"#"+heading, i+1
	}
	flush(len(lines))
	return out
}

func paragraphBlocks(base string, lines []string) []Chunk {
	var out []Chunk
	start := 1
	for i := 0; i < len(lines); i++ {
		atBreak := strings.TrimSpace(lines[i]) == ""
		n := i + 1 - (start - 1)
		if (atBreak && n >= blockLines) || i == len(lines)-1 {
			part := lines[start-1 : i+1]
			if !blank(part) {
				out = append(out, textChunk("block", base+":"+strconv.Itoa(start), part, start))
			}
			start = i + 2
		}
	}
	return out
}
