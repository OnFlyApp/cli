package vectorstore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

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

func distinctPaths(chunks []EmbeddedChunk) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, c := range chunks {
		path := slashPath(c.Chunk.FilePath)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}
