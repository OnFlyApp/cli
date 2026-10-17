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

// DocFreq counts the chunks containing term, and all chunks. The count stops at
// 5% of the chunks: a word that common is weighed the same whatever its exact
// count, and counting a very common word through the full-text index is the
// slowest thing a query does. The total is kept until the index changes.
func (s *SQLite) DocFreq(ctx context.Context, term, workspace string) (int, int, error) {
	n, err := s.chunkTotal(ctx, workspace)
	if err != nil {
		return 0, 0, err
	}
	limit := max(200, n/20)
	var df int
	err = s.conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT 1 FROM chunk_fts JOIN chunk_meta m ON m.rowid = chunk_fts.rowid
		WHERE chunk_fts MATCH ? AND (? = '' OR ? = '*' OR m.workspace = ?)`+sqliteNoProse(ctx, "m.file_path")+`
		LIMIT ?)`, "content : "+ftsPhrase(term), workspace, workspace, workspace, limit).Scan(&df)
	return df, n, err
}

type totalKey struct {
	workspace string
	noProse   bool
}

type totalVal struct {
	generation int64
	n          int
}

func (s *SQLite) chunkTotal(ctx context.Context, workspace string) (int, error) {
	gen, err := s.Generation(ctx)
	if err != nil {
		return 0, err
	}
	key := totalKey{workspace, excludesProse(ctx)}
	s.totalMu.Lock()
	v, ok := s.totals[key]
	s.totalMu.Unlock()
	if ok && v.generation == gen {
		return v.n, nil
	}
	var n int
	if err := s.conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM chunk_meta
		WHERE (? = '' OR ? = '*' OR workspace = ?)`+sqliteNoProse(ctx, "file_path"), workspace, workspace, workspace).Scan(&n); err != nil {
		return 0, err
	}
	s.totalMu.Lock()
	if s.totals == nil {
		s.totals = map[totalKey]totalVal{}
	}
	s.totals[key] = totalVal{gen, n}
	s.totalMu.Unlock()
	return n, nil
}

func pgNoProse(ctx context.Context) string {
	if !excludesProse(ctx) {
		return ""
	}
	return ` AND file_path !~* '\.(md|mdx|rst|txt|adoc)$'`
}
