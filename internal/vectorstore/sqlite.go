package vectorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/axitdev/onfly/internal/parser"
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

const schemaVersion = "2"

type SQLite struct {
	db         *sql.DB
	dimensions int
}

func OpenSQLite(dsn string) (*SQLite, error) {
	openDSN := dsn
	if dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(dsn)
		if err != nil {
			return nil, err
		}
		openDSN = sqliteFileURI(abs)
	}
	db, err := sql.Open("sqlite", openDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if !memoryDSN(dsn) {
		if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &SQLite{db: db}, nil
}

func sqliteFileURI(abs string) string {
	// ToSlash keeps '\' on Unix. SQLite file URIs need forward slashes.
	abs = strings.ReplaceAll(filepath.ToSlash(abs), `\`, "/")
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"}
	return u.String()
}

func memoryDSN(dsn string) bool {
	if dsn == ":memory:" || strings.Contains(dsn, "mode=memory") {
		return true
	}
	if strings.HasPrefix(dsn, "file:") {
		u, err := url.Parse(dsn)
		if err == nil && (u.Path == "/:memory:" || u.Path == ":memory:" || u.Query().Get("mode") == "memory") {
			return true
		}
	}
	return false
}

func (s *SQLite) Init(ctx context.Context, dimensions int) error {
	if dimensions < 1 || dimensions > 8192 {
		return fmt.Errorf("vectorstore: dimensions %d out of range", dimensions)
	}
	existing, version, err := s.readMeta(ctx, dimensions)
	if err != nil {
		return err
	}
	if err := s.ensureTables(ctx, version); err != nil {
		return err
	}
	if err := s.ensureCosine(ctx, dimensions); err != nil {
		return err
	}
	if width, ok, err := vecWidth(ctx, s.db); err != nil {
		return err
	} else if ok && width != dimensions {
		return fmt.Errorf("%w: database %d, config %d", ErrDimensionMismatch, width, dimensions)
	}
	if err := s.writeMeta(ctx, existing, version, dimensions); err != nil {
		return err
	}
	s.dimensions = dimensions
	return nil
}

// readMeta returns the stored dimensions and schema version, rejecting a
// mismatch or a version this build cannot migrate.
func (s *SQLite) readMeta(ctx context.Context, dimensions int) (existing, version string, err error) {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return "", "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = 'dimensions'`).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if err == nil && existing != fmt.Sprint(dimensions) {
		return "", "", fmt.Errorf("%w: database %s, config %d", ErrDimensionMismatch, existing, dimensions)
	}
	version, err = s.meta(ctx, "version")
	if err != nil {
		return "", "", err
	}
	if version != "" && version != schemaVersion {
		return "", "", fmt.Errorf("vectorstore: schema version %s cannot be migrated; rebuild the index", version)
	}
	return existing, version, nil
}

// ensureTables creates or migrates chunk_meta, the FTS table, triggers and index_state.
func (s *SQLite) ensureTables(ctx context.Context, version string) error {
	exists, err := sqliteTable(ctx, s.db, "chunk_meta")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := s.db.ExecContext(ctx, `CREATE TABLE chunk_meta (
			rowid INTEGER PRIMARY KEY,
			file_path TEXT NOT NULL,
			type TEXT NOT NULL,
			content TEXT NOT NULL,
			metadata TEXT NOT NULL,
			outbound TEXT NOT NULL DEFAULT '[]',
			workspace TEXT NOT NULL DEFAULT ''
		)`); err != nil {
			return err
		}
	} else if version != schemaVersion {
		if err := s.migrateV1(ctx); err != nil {
			return err
		}
	}
	hadFTS, err := sqliteTable(ctx, s.db, "chunk_fts")
	if err != nil {
		return err
	}
	if hadFTS {
		// An index built with the default tokenizer cannot answer substring
		// queries. Drop it so the rebuild below recreates it with trigrams.
		var create string
		if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name = 'chunk_fts'`).Scan(&create); err != nil {
			return err
		}
		if !strings.Contains(strings.ToLower(create), "trigram") {
			if _, err := s.db.ExecContext(ctx, `DROP TABLE chunk_fts`); err != nil {
				return err
			}
			hadFTS = false
		}
	}
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_chunk_meta_file ON chunk_meta(file_path)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS chunk_fts USING fts5(
			content, file_path, content='chunk_meta', content_rowid='rowid', tokenize='trigram'
		)`,
		`CREATE TRIGGER IF NOT EXISTS chunk_meta_ai AFTER INSERT ON chunk_meta BEGIN
			INSERT INTO chunk_fts(rowid, content, file_path) VALUES (new.rowid, new.content, new.file_path);
		END`,
		`CREATE TRIGGER IF NOT EXISTS chunk_meta_ad AFTER DELETE ON chunk_meta BEGIN
			INSERT INTO chunk_fts(chunk_fts, rowid, content, file_path) VALUES ('delete', old.rowid, old.content, old.file_path);
		END`,
		`CREATE TABLE IF NOT EXISTS index_state (
			file_path TEXT PRIMARY KEY,
			content_hash TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if !hadFTS {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO chunk_fts(chunk_fts) VALUES('rebuild')`); err != nil {
			return err
		}
	}
	return nil
}

// writeMeta records dimensions, schema version and the generation counter.
func (s *SQLite) writeMeta(ctx context.Context, existing, version string, dimensions int) error {
	if existing == "" {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('dimensions', ?)
			ON CONFLICT(key) DO NOTHING`, fmt.Sprint(dimensions)); err != nil {
			return err
		}
		var stored string
		if err := s.db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = 'dimensions'`).Scan(&stored); err != nil {
			return err
		}
		if stored != fmt.Sprint(dimensions) {
			return fmt.Errorf("%w: database %s, config %d", ErrDimensionMismatch, stored, dimensions)
		}
	}
	if version != schemaVersion {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('version', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, schemaVersion); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('index_generation', '0')
		ON CONFLICT(key) DO NOTHING`); err != nil {
		return err
	}
	return nil
}

func cosineVecSQL(dimensions int) string {
	return fmt.Sprintf(`CREATE VIRTUAL TABLE chunk_vecs USING vec0(embedding float[%d] distance_metric=cosine)`, dimensions)
}

// ensureCosine creates chunk_vecs with cosine distance. An existing L2 vec0
// table is rebuilt in place: rowid and embedding are copied, and schema_meta
// is left unchanged.
func (s *SQLite) ensureCosine(ctx context.Context, dimensions int) error {
	var create string
	err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name = 'chunk_vecs'`).Scan(&create)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.ExecContext(ctx, cosineVecSQL(dimensions))
		return err
	}
	if err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(create), "distance_metric=cosine") {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS chunk_vecs_l2_copy`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE chunk_vecs_l2_copy(rowid INTEGER PRIMARY KEY, embedding TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO chunk_vecs_l2_copy(rowid, embedding) SELECT rowid, vec_to_json(embedding) FROM chunk_vecs`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE chunk_vecs`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, cosineVecSQL(dimensions)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO chunk_vecs(rowid, embedding) SELECT rowid, embedding FROM chunk_vecs_l2_copy`); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DROP TABLE chunk_vecs_l2_copy`)
	return err
}

func (s *SQLite) migrateV1(ctx context.Context) error {
	for _, col := range []struct{ name, ddl string }{
		{"outbound", `ALTER TABLE chunk_meta ADD COLUMN outbound TEXT NOT NULL DEFAULT '[]'`},
		{"workspace", `ALTER TABLE chunk_meta ADD COLUMN workspace TEXT NOT NULL DEFAULT ''`},
	} {
		ok, err := sqliteColumn(ctx, s.db, "chunk_meta", col.name)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if _, err := s.db.ExecContext(ctx, col.ddl); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE chunk_meta SET file_path = replace(file_path, '\', '/') WHERE instr(file_path, '\') > 0`)
	return err
}

func (s *SQLite) CommitFile(ctx context.Context, path, hash string, chunks []EmbeddedChunk) error {
	if len(chunks) > 0 {
		if err := checkVectors(chunks, s.dimensions); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.deleteChunkRows(ctx, tx, []string{path}); err != nil {
		return err
	}
	if err := insertSQLiteChunks(ctx, tx, chunks); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO index_state(file_path, content_hash) VALUES (?, ?)
		ON CONFLICT(file_path) DO UPDATE SET content_hash = excluded.content_hash`, slashPath(path), hash); err != nil {
		return err
	}
	if err := bumpSQLite(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSQLiteChunks(ctx context.Context, tx *sql.Tx, chunks []EmbeddedChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	meta, err := tx.PrepareContext(ctx, `INSERT INTO chunk_meta(file_path, type, content, metadata, outbound, workspace) VALUES (?, ?, ?, ?, ?, ?) RETURNING rowid`)
	if err != nil {
		return err
	}
	defer meta.Close()
	vec, err := tx.PrepareContext(ctx, `INSERT INTO chunk_vecs(rowid, embedding) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer vec.Close()
	for _, c := range chunks {
		var id int64
		if err := meta.QueryRowContext(ctx, slashPath(c.Chunk.FilePath), c.Chunk.Type, c.Chunk.Content,
			metaJSON(c.Chunk.Metadata), outboundJSON(c.Chunk.Outbound), c.Workspace).Scan(&id); err != nil {
			return err
		}
		if _, err := vec.ExecContext(ctx, id, vecLiteral(c.Vector)); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) DeleteFiles(ctx context.Context, filePaths []string) error {
	if len(filePaths) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.deletePaths(ctx, tx, filePaths); err != nil {
		return err
	}
	return tx.Commit()
}

const sqliteBindBatch = 500

func (s *SQLite) deletePaths(ctx context.Context, tx *sql.Tx, paths []string) error {
	if err := s.deleteChunkRows(ctx, tx, paths); err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	for batch := range slices.Chunk(paths, sqliteBindBatch) {
		args := make([]any, len(batch))
		for i, p := range batch {
			args[i] = slashPath(p)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM index_state WHERE file_path IN (`+placeholders(len(batch))+`)`, args...); err != nil {
			return err
		}
	}
	return bumpSQLite(ctx, tx)
}

func (s *SQLite) deleteChunkRows(ctx context.Context, tx *sql.Tx, paths []string) error {
	for batch := range slices.Chunk(paths, sqliteBindBatch) {
		if err := s.deleteChunkBatch(ctx, tx, batch); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) deleteChunkBatch(ctx context.Context, tx *sql.Tx, paths []string) error {
	args := make([]any, len(paths))
	for i, p := range paths {
		args[i] = slashPath(p)
	}
	in := placeholders(len(paths))
	rows, err := tx.QueryContext(ctx, `SELECT rowid FROM chunk_meta WHERE file_path IN (`+in+`)`, args...)
	if err != nil {
		return err
	}
	var ids []any
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for batch := range slices.Chunk(ids, sqliteBindBatch) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_vecs WHERE rowid IN (`+placeholders(len(batch))+`)`, batch...); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM chunk_meta WHERE file_path IN (`+in+`)`, args...)
	return err
}

func (s *SQLite) ListFiles(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT file_path FROM chunk_meta ORDER BY file_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, slashPath(p))
	}
	return out, rows.Err()
}

func (s *SQLite) SearchSemantic(ctx context.Context, queryVector []float32, limit int, workspace string) ([]parser.Chunk, error) {
	if len(queryVector) != s.dimensions {
		return nil, fmt.Errorf("vectorstore: vector length %d, want %d", len(queryVector), s.dimensions)
	}
	if limit < 1 {
		return nil, fmt.Errorf("vectorstore: limit must be positive")
	}
	var rows *sql.Rows
	var err error
	if workspace != "" && workspace != "*" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT m.file_path, m.type, m.content, m.metadata, m.outbound
			FROM chunk_vecs
			JOIN chunk_meta m ON m.rowid = chunk_vecs.rowid
			WHERE embedding MATCH ? AND k = ?
			  AND chunk_vecs.rowid IN (SELECT rowid FROM chunk_meta WHERE workspace = ?)
			ORDER BY distance
			LIMIT ?`, vecLiteral(queryVector), limit, workspace, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT m.file_path, m.type, m.content, m.metadata, m.outbound
			FROM (
				SELECT rowid, distance FROM chunk_vecs
				WHERE embedding MATCH ?
				ORDER BY distance
				LIMIT ?
			) v
			JOIN chunk_meta m ON m.rowid = v.rowid
			ORDER BY v.distance
			LIMIT ?`, vecLiteral(queryVector), limit, limit)
	}
	if err != nil {
		return nil, err
	}
	return scanChunks(rows)
}

// SearchKeyword is a case-insensitive substring match. Keywords of three or
// more characters use the trigram FTS index; shorter ones scan chunk_meta.
func (s *SQLite) SearchKeyword(ctx context.Context, keyword string, limit int, workspace string) ([]parser.Chunk, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, fmt.Errorf("vectorstore: empty keyword")
	}
	if limit < 1 {
		return nil, fmt.Errorf("vectorstore: limit must be positive")
	}
	var rows *sql.Rows
	var err error
	if utf8.RuneCountInString(keyword) >= 3 {
		rows, err = s.db.QueryContext(ctx, `
			SELECT m.file_path, m.type, m.content, m.metadata, m.outbound
			FROM chunk_fts
			JOIN chunk_meta m ON m.rowid = chunk_fts.rowid
			WHERE chunk_fts MATCH ?
			  AND (? = '' OR ? = '*' OR m.workspace = ?)
			ORDER BY chunk_fts.rank
			LIMIT ?`, "content : "+ftsPhrase(keyword), workspace, workspace, workspace, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT file_path, type, content, metadata, outbound FROM chunk_meta
			WHERE instr(lower(content), lower(?)) > 0
			  AND (? = '' OR ? = '*' OR workspace = ?)
			LIMIT ?`, keyword, workspace, workspace, workspace, limit)
	}
	if err != nil {
		return nil, err
	}
	return scanChunks(rows)
}

func (s *SQLite) GetComponentAST(ctx context.Context, name string) (*parser.Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT file_path, type, content, metadata, outbound FROM chunk_meta
		WHERE json_extract(metadata, '$.name') = ?
		ORDER BY rowid`, name)
	if err != nil {
		return nil, err
	}
	chunks, err := scanChunks(rows)
	if err != nil {
		return nil, err
	}
	return assembleComponent(chunks)
}

func (s *SQLite) ListChunks(ctx context.Context) ([]StoredChunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rowid, file_path, type, content, metadata, outbound FROM chunk_meta
		ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredChunk
	for rows.Next() {
		var id int64
		var filePath, typ, content, meta, outbound string
		if err := rows.Scan(&id, &filePath, &typ, &content, &meta, &outbound); err != nil {
			return nil, err
		}
		ch, err := chunkFrom(filePath, typ, content, meta, outbound)
		if err != nil {
			return nil, err
		}
		out = append(out, StoredChunk{ID: id, Chunk: ch})
	}
	return out, rows.Err()
}

func (s *SQLite) ChunksByFiles(ctx context.Context, paths []string) ([]StoredChunk, error) {
	var out []StoredChunk
	for batch := range slices.Chunk(paths, sqliteBindBatch) {
		args := make([]any, len(batch))
		for i, p := range batch {
			args[i] = slashPath(p)
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT rowid, file_path, type, content, metadata, outbound FROM chunk_meta
			WHERE file_path IN (`+placeholders(len(batch))+`) ORDER BY rowid`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var filePath, typ, content, meta, outbound string
			if err := rows.Scan(&id, &filePath, &typ, &content, &meta, &outbound); err != nil {
				rows.Close()
				return nil, err
			}
			ch, err := chunkFrom(filePath, typ, content, meta, outbound)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, StoredChunk{ID: id, Chunk: ch})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLite) Names(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT json_extract(metadata, '$.name') FROM chunk_meta
		WHERE coalesce(json_extract(metadata, '$.name'), '') <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *SQLite) UpdateOutbound(ctx context.Context, outbound map[int64][]string) error {
	if len(outbound) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, refs := range outbound {
		if _, err := tx.ExecContext(ctx, `UPDATE chunk_meta SET outbound = ? WHERE rowid = ?`, outboundJSON(refs), id); err != nil {
			return err
		}
	}
	if err := bumpSQLite(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) GetReferences(ctx context.Context, name string) ([]parser.Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.file_path, m.type, m.content, m.metadata, m.outbound
		FROM chunk_meta AS m, json_each(m.outbound)
		WHERE json_each.value = ?
		ORDER BY m.file_path, m.rowid
		LIMIT 100`, name)
	if err != nil {
		return nil, err
	}
	return scanChunks(rows)
}

func (s *SQLite) WorkspacesForName(ctx context.Context, name string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT workspace FROM chunk_meta
		WHERE json_extract(metadata, '$.name') = ? AND workspace != ''
		ORDER BY workspace`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var ws string
		if err := rows.Scan(&ws); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (s *SQLite) FileHash(ctx context.Context, path string) (string, bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT content_hash FROM index_state WHERE file_path = ?`, slashPath(path)).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}

func (s *SQLite) FileHashes(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_path, content_hash FROM index_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var path, hash string
		if err := rows.Scan(&path, &hash); err != nil {
			return nil, err
		}
		out[slashPath(path)] = hash
	}
	return out, rows.Err()
}

func (s *SQLite) Generation(ctx context.Context) (int64, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = 'index_generation'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconvParse(raw)
}

func (s *SQLite) Optimize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO chunk_fts(chunk_fts) VALUES('optimize')`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}

func (s *SQLite) CheckMeta(ctx context.Context, dimensions int) error {
	version, err := s.meta(ctx, "version")
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	if version != "" && version != schemaVersion {
		return fmt.Errorf("vectorstore: schema version %s cannot be migrated; rebuild the index", version)
	}
	dims, err := s.meta(ctx, "dimensions")
	if err != nil {
		return err
	}
	if dims != "" && dims != fmt.Sprint(dimensions) {
		return fmt.Errorf("%w: database %s, config %d", ErrDimensionMismatch, dims, dimensions)
	}
	return nil
}

func (s *SQLite) meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func vecWidth(ctx context.Context, db *sql.DB) (int, bool, error) {
	var create string
	err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name = 'chunk_vecs'`).Scan(&create)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	i := strings.Index(create, "float[")
	if i < 0 {
		return 0, false, nil
	}
	rest := create[i+len("float["):]
	j := strings.IndexByte(rest, ']')
	if j < 0 {
		return 0, false, nil
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		return 0, false, nil
	}
	return n, true, nil
}

func sqliteTable(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var got string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func sqliteColumn(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func bumpSQLite(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE schema_meta SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT) WHERE key = 'index_generation'`)
	return err
}

func strconvParse(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("vectorstore: generation %q: %w", raw, err)
	}
	return n, nil
}

func scanChunks(rows *sql.Rows) ([]parser.Chunk, error) {
	defer rows.Close()
	var out []parser.Chunk
	for rows.Next() {
		var filePath, typ, content, meta, outbound string
		if err := rows.Scan(&filePath, &typ, &content, &meta, &outbound); err != nil {
			return nil, err
		}
		ch, err := chunkFrom(filePath, typ, content, meta, outbound)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// UnnamedChunks counts chunks without metadata.name, which get_component_ast cannot find.
func (s *SQLite) UnnamedChunks(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM chunk_meta
		WHERE coalesce(json_extract(metadata, '$.name'), '') = ''`).Scan(&n)
	return n, err
}

// Stats reports file, chunk, type and workspace counts.
func (s *SQLite) Stats(ctx context.Context) (IndexStats, error) {
	out := IndexStats{ByType: map[string]int{}, Workspaces: map[string]int{}}
	if err := s.db.QueryRowContext(ctx, `SELECT count(DISTINCT file_path) FROM chunk_meta`).Scan(&out.Files); err != nil {
		return out, err
	}
	groups := []struct {
		query string
		into  map[string]int
	}{
		{`SELECT type, count(*) FROM chunk_meta GROUP BY type`, out.ByType},
		{`SELECT workspace, count(*) FROM chunk_meta GROUP BY workspace`, out.Workspaces},
	}
	for _, g := range groups {
		rows, err := s.db.QueryContext(ctx, g.query)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var key string
			var n int
			if err := rows.Scan(&key, &n); err != nil {
				rows.Close()
				return out, err
			}
			g.into[key] = n
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return out, err
		}
		rows.Close()
	}
	for _, n := range out.ByType {
		out.Chunks += n
	}
	var err error
	out.Unnamed, err = s.UnnamedChunks(ctx)
	return out, err
}
