package vectorstore

import (
	"context"
	"path"
	"strings"
)

type proseKey struct{}

// WithoutProse marks a context so that keyword search and term statistics leave
// out documentation files (Markdown, text). The filter runs in the query, so
// prose cannot fill the candidate limit before ranking sees any code.
func WithoutProse(ctx context.Context) context.Context {
	return context.WithValue(ctx, proseKey{}, true)
}

func excludesProse(ctx context.Context) bool {
	v, _ := ctx.Value(proseKey{}).(bool)
	return v
}

var proseExts = []string{".md", ".mdx", ".rst", ".txt", ".adoc"}

// IsProse reports whether a path is a documentation file.
func IsProse(p string) bool {
	e := strings.ToLower(path.Ext(p))
	for _, x := range proseExts {
		if e == x {
			return true
		}
	}
	return false
}

// sqliteNoProse is an AND clause for a SQLite column holding a file path.
func sqliteNoProse(ctx context.Context, col string) string {
	if !excludesProse(ctx) {
		return ""
	}
	var b strings.Builder
	for _, x := range proseExts {
		b.WriteString(" AND lower(" + col + ") NOT GLOB '*" + x + "'")
	}
	return b.String()
}

// TermStats gives the rarity of a word in the index: the chunks containing it,
// out of all chunks. A store without it ranks every word the same.
type TermStats interface {
	DocFreq(ctx context.Context, term, workspace string) (df, n int, err error)
}

func (s *SQLite) DocFreq(ctx context.Context, term, workspace string) (int, int, error) {
	var n, df int
	noProse := sqliteNoProse(ctx, "file_path")
	if err := s.conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM chunk_meta
		WHERE (? = '' OR ? = '*' OR workspace = ?)`+noProse, workspace, workspace, workspace).Scan(&n); err != nil {
		return 0, 0, err
	}
	err := s.conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM chunk_fts
		JOIN chunk_meta m ON m.rowid = chunk_fts.rowid
		WHERE chunk_fts MATCH ? AND (? = '' OR ? = '*' OR m.workspace = ?)`+sqliteNoProse(ctx, "m.file_path"),
		"content : "+ftsPhrase(term), workspace, workspace, workspace).Scan(&df)
	return df, n, err
}

func pgNoProse(ctx context.Context) string {
	if !excludesProse(ctx) {
		return ""
	}
	return ` AND file_path !~* '\.(md|mdx|rst|txt|adoc)$'`
}
