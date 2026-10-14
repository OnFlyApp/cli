package vectorstore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/axitdev/onfly/internal/parser"
)

func vecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// parseVecLiteral is the inverse of vecLiteral: "[1,2.5,-3]".
func parseVecLiteral(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("vectorstore: bad vector literal")
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	if body == "" {
		return []float32{}, nil
	}
	parts := strings.Split(body, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}

func metaJSON(m map[string]string) string {
	if m == nil {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func scanMeta(raw string) (map[string]string, error) {
	if raw == "" || raw == "null" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

func slashPath(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), `\`, "/")
}

func outboundJSON(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func scanOutbound(raw string) ([]string, error) {
	if raw == "" || raw == "null" {
		return []string{}, nil
	}
	var v []string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	if v == nil {
		v = []string{}
	}
	return v, nil
}

// assembleComponents groups rows by file and receiver and assembles each group.
func assembleComponents(chunks []parser.Chunk) ([]parser.Chunk, error) {
	if len(chunks) == 0 {
		return nil, ErrNotFound
	}
	type key struct{ file, receiver string }
	var order []key
	groups := map[key][]parser.Chunk{}
	for _, c := range chunks {
		k := key{c.FilePath, ""}
		if c.Metadata != nil {
			k.receiver = c.Metadata["receiver"]
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], c)
	}
	out := make([]parser.Chunk, 0, len(order))
	for _, k := range order {
		c, err := assembleComponent(groups[k])
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

// assembleComponent returns the earliest chunk with a name. Later split parts
// of that same file are joined in part order. The overlap shared by two
// adjacent parts is written once.
func assembleComponent(chunks []parser.Chunk) (*parser.Chunk, error) {
	if len(chunks) == 0 {
		return nil, ErrNotFound
	}
	first := chunks[0]
	name := metaName(first)
	group := make([]parser.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if c.FilePath == first.FilePath && metaName(c) == name {
			group = append(group, c)
		}
	}
	if len(group) == 1 {
		c := group[0]
		return &c, nil
	}
	sort.SliceStable(group, func(i, j int) bool {
		return partIndex(group[i]) < partIndex(group[j])
	})
	out := group[0]
	meta := map[string]string{}
	for k, v := range group[0].Metadata {
		if k != "part" {
			meta[k] = v
		}
	}
	content := group[0].Content
	for _, part := range group[1:] {
		content = joinOverlap(content, part.Content)
	}
	seen := map[string]bool{}
	var outbound []string
	for _, part := range group {
		for _, ref := range part.Outbound {
			if !seen[ref] {
				seen[ref] = true
				outbound = append(outbound, ref)
			}
		}
	}
	sort.Strings(outbound)
	out.Outbound = outbound
	if end := group[len(group)-1].Metadata["end_line"]; end != "" {
		meta["end_line"] = end
	}
	out.Content = content
	out.Metadata = meta
	return &out, nil
}

func metaName(c parser.Chunk) string {
	if c.Metadata == nil {
		return ""
	}
	return c.Metadata["name"]
}

func partIndex(c parser.Chunk) int {
	if c.Metadata == nil {
		return 0
	}
	raw, _, _ := strings.Cut(c.Metadata["part"], "/")
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

func joinOverlap(prev, next string) string {
	max := len(prev)
	if len(next) < max {
		max = len(next)
	}
	for n := max; n > 0; n-- {
		if !utf8.ValidString(next[:n]) {
			continue
		}
		if strings.HasSuffix(prev, next[:n]) {
			return prev + next[n:]
		}
	}
	return prev + next
}

func chunkFrom(filePath, typ, content, meta, outbound string) (parser.Chunk, error) {
	m, err := scanMeta(meta)
	if err != nil {
		return parser.Chunk{}, err
	}
	refs, err := scanOutbound(outbound)
	if err != nil {
		return parser.Chunk{}, err
	}
	return parser.Chunk{
		FilePath: slashPath(filePath),
		Type:     typ,
		Content:  content,
		Metadata: m,
		Outbound: refs,
	}, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func checkVectors(chunks []EmbeddedChunk, dimensions int) error {
	for _, c := range chunks {
		if len(c.Vector) != dimensions {
			return fmt.Errorf("vectorstore: vector length %d, want %d", len(c.Vector), dimensions)
		}
	}
	return nil
}

func likePattern(keyword string) string {
	keyword = strings.ReplaceAll(keyword, `\`, `\\`)
	keyword = strings.ReplaceAll(keyword, `%`, `\%`)
	keyword = strings.ReplaceAll(keyword, `_`, `\_`)
	return "%" + keyword + "%"
}

func ftsPhrase(keyword string) string {
	return `"` + strings.ReplaceAll(keyword, `"`, `""`) + `"`
}
