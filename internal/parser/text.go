package parser

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
)

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
	seen := map[string]int{}
	name, start := base, 1
	flush := func(end int) {
		part := lines[start-1 : end]
		if !blank(part) {
			out = append(out, textChunk("section", name, part, start))
		}
	}
	i := 0
	if len(lines) > 0 && lines[0] == "---" {
		for j := 1; j < len(lines); j++ {
			if lines[j] == "---" || lines[j] == "..." {
				i = j + 1
				break
			}
		}
	}
	var fenceCh byte
	fenceN := 0
	for ; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if trim != "" && (trim[0] == '`' || trim[0] == '~') {
			n := len(trim) - len(strings.TrimLeft(trim, trim[:1]))
			if fenceN == 0 && n >= 3 {
				fenceCh, fenceN = trim[0], n
				continue
			}
			if fenceN > 0 && trim[0] == fenceCh && n >= fenceN {
				fenceN = 0
				continue
			}
		}
		if fenceN > 0 || !strings.HasPrefix(line, "#") {
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
		if seen[name]++; seen[name] > 1 {
			name += "~" + strconv.Itoa(seen[name])
		}
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
