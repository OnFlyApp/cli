package vectorstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool       *pgxpool.Pool
	dimensions int
	extra      map[string]int
}

func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

const postgresHNSWMaxDim = 2000

func (p *Postgres) Init(ctx context.Context, dimensions int) error {
	if dimensions < 1 || dimensions > 8192 {
		return fmt.Errorf("vectorstore: dimensions %d out of range", dimensions)
	}
	if _, err := p.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(874213)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	existing, err := metaTx(ctx, tx, "dimensions")
	if err != nil {
		return err
	}
	if existing != "" && existing != fmt.Sprint(dimensions) {
		return fmt.Errorf("%w: database %s, config %d", ErrDimensionMismatch, existing, dimensions)
	}
	version, err := metaTx(ctx, tx, "version")
	if err != nil {
		return err
	}
	if version != "" && version != schemaVersion {
		return fmt.Errorf("vectorstore: schema version %s cannot be migrated; rebuild the index", version)
	}
	if width, ok, err := embeddingWidth(ctx, tx); err != nil {
		return err
	} else if ok && width != dimensions {
		return fmt.Errorf("%w: database %d, config %d", ErrDimensionMismatch, width, dimensions)
	}
	for _, stmt := range postgresSchema(dimensions) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	if width, ok, err := embeddingWidth(ctx, tx); err != nil {
		return err
	} else if !ok || width != dimensions {
		if !ok {
			return fmt.Errorf("vectorstore: chunks.embedding is missing")
		}
		return fmt.Errorf("%w: database %d, config %d", ErrDimensionMismatch, width, dimensions)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_meta(key, value) VALUES ('dimensions', $1)
		ON CONFLICT(key) DO NOTHING`, fmt.Sprint(dimensions)); err != nil {
		return err
	}
	stored, err := metaTx(ctx, tx, "dimensions")
	if err != nil {
		return err
	}
	if stored != fmt.Sprint(dimensions) {
		return fmt.Errorf("%w: database %s, config %d", ErrDimensionMismatch, stored, dimensions)
	}
	if version != schemaVersion {
		if _, err := tx.Exec(ctx, `UPDATE chunks SET file_path = replace(file_path, chr(92), '/') WHERE strpos(file_path, chr(92)) > 0`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_meta(key, value) VALUES ('version', $1)
			ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value`, schemaVersion); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_meta(key, value) VALUES ('index_generation', '0')
		ON CONFLICT(key) DO NOTHING`); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	p.dimensions = dimensions
	return nil
}

func postgresSchema(dimensions int) []string {
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS chunks (
			id BIGSERIAL PRIMARY KEY,
			file_path TEXT NOT NULL,
			type TEXT NOT NULL,
			content TEXT NOT NULL,
			metadata JSONB NOT NULL,
			outbound JSONB NOT NULL DEFAULT '[]',
			workspace TEXT NOT NULL DEFAULT '',
			embedding vector(%d),
			tsv tsvector GENERATED ALWAYS AS (
				to_tsvector('simple', coalesce(content, '') || ' ' || coalesce(file_path, ''))
			) STORED
		)`, dimensions),
		`ALTER TABLE chunks ADD COLUMN IF NOT EXISTS outbound JSONB NOT NULL DEFAULT '[]'`,
		`ALTER TABLE chunks ADD COLUMN IF NOT EXISTS workspace TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS chunks_file_idx ON chunks (file_path)`,
		`CREATE INDEX IF NOT EXISTS chunks_name_idx ON chunks ((metadata->>'name'))`,
		`CREATE INDEX IF NOT EXISTS chunks_tsv_idx ON chunks USING gin (tsv)`,
		`CREATE TABLE IF NOT EXISTS index_state (
			file_path TEXT PRIMARY KEY,
			content_hash TEXT NOT NULL
		)`,
	}
	if dimensions <= postgresHNSWMaxDim {
		stmts = append(stmts, `CREATE INDEX IF NOT EXISTS chunks_embedding_idx ON chunks USING hnsw (embedding vector_cosine_ops)`)
	}
	return stmts
}

func embeddingWidth(ctx context.Context, tx pgx.Tx) (int, bool, error) {
	var format string
	err := tx.QueryRow(ctx, `
		SELECT pg_catalog.format_type(a.atttypid, a.atttypmod)
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'chunks' AND n.nspname = current_schema()
		  AND a.attname = 'embedding' AND a.attnum > 0 AND NOT a.attisdropped`).Scan(&format)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	n, ok := vectorWidth(format)
	if !ok {
		return 0, false, fmt.Errorf("vectorstore: unexpected embedding type %s", format)
	}
	return n, true, nil
}

func vectorWidth(format string) (int, bool) {
	if i := strings.LastIndex(format, "."); i >= 0 {
		format = format[i+1:]
	}
	const prefix = "vector("
	if !strings.HasPrefix(format, prefix) || !strings.HasSuffix(format, ")") {
		return 0, false
	}
	n, err := strconv.Atoi(format[len(prefix) : len(format)-1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func metaTx(ctx context.Context, tx pgx.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRow(ctx, `SELECT value FROM schema_meta WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (p *Postgres) CheckMeta(ctx context.Context, dimensions int) error {
	version, err := p.meta(ctx, "version")
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil
		}
		return err
	}
	if version != "" && version != schemaVersion {
		return fmt.Errorf("vectorstore: schema version %s cannot be migrated; rebuild the index", version)
	}
	dims, err := p.meta(ctx, "dimensions")
	if err != nil {
		return err
	}
	if dims != "" && dims != fmt.Sprint(dimensions) {
		return fmt.Errorf("%w: database %s, config %d", ErrDimensionMismatch, dims, dimensions)
	}
	return nil
}

func (p *Postgres) meta(ctx context.Context, key string) (string, error) {
	var value string
	err := p.pool.QueryRow(ctx, `SELECT value FROM schema_meta WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (p *Postgres) CommitFile(ctx context.Context, path, hash string, chunks []EmbeddedChunk) error {
	if len(chunks) > 0 {
		if err := checkVectors(chunks, p.dimensions); err != nil {
			return err
		}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	path = slashPath(path)
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE file_path = $1`, path); err != nil {
		return err
	}
	if err := insertPGChunks(ctx, tx, chunks, p.extra); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO index_state(file_path, content_hash) VALUES ($1, $2)
		ON CONFLICT(file_path) DO UPDATE SET content_hash = EXCLUDED.content_hash`, path, hash); err != nil {
		return err
	}
	if err := bumpPostgres(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertPGChunks(ctx context.Context, tx pgx.Tx, chunks []EmbeddedChunk, extra map[string]int) error {
	if len(chunks) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range chunks {
		cols := `file_path, type, content, metadata, outbound, workspace, embedding`
		vals := `$1, $2, $3, $4::jsonb, $5::jsonb, $6, $7::vector`
		args := []any{slashPath(c.Chunk.FilePath), c.Chunk.Type, c.Chunk.Content, metaJSON(c.Chunk.Metadata), outboundJSON(c.Chunk.Outbound), c.Workspace, vecLiteral(c.Vector)}
		slots := make([]string, 0, len(c.Extra))
		for slot := range c.Extra {
			slots = append(slots, slot)
		}
		sort.Strings(slots)
		for _, slot := range slots {
			dims, ok := extra[slot]
			if !ok {
				return fmt.Errorf("vectorstore: extra slot %q is not initialised", slot)
			}
			if len(c.Extra[slot]) != dims {
				return fmt.Errorf("vectorstore: slot %q vector length %d, want %d", slot, len(c.Extra[slot]), dims)
			}
			args = append(args, vecLiteral(c.Extra[slot]))
			cols += ", emb_" + slot
			vals += fmt.Sprintf(", $%d::vector", len(args))
		}
		batch.Queue(`INSERT INTO chunks(`+cols+`) VALUES (`+vals+`)`, args...)
	}
	res := tx.SendBatch(ctx, batch)
	for range chunks {
		if _, err := res.Exec(); err != nil {
			_ = res.Close()
			return err
		}
	}
	return res.Close()
}

func (p *Postgres) DeleteFiles(ctx context.Context, filePaths []string) error {
	if len(filePaths) == 0 {
		return nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := p.deletePaths(ctx, tx, filePaths); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) deletePaths(ctx context.Context, tx pgx.Tx, paths []string) error {
	paths = append([]string(nil), paths...)
	for i, path := range paths {
		paths[i] = slashPath(path)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE file_path = ANY($1)`, paths); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM index_state WHERE file_path = ANY($1)`, paths); err != nil {
		return err
	}
	return bumpPostgres(ctx, tx)
}

func (p *Postgres) ListFiles(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT file_path FROM chunks ORDER BY file_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		out = append(out, slashPath(path))
	}
	return out, rows.Err()
}

func (p *Postgres) SearchSemantic(ctx context.Context, queryVector []float32, limit int, workspace string) ([]parser.Chunk, error) {
	if len(queryVector) != p.dimensions {
		return nil, fmt.Errorf("vectorstore: vector length %d, want %d", len(queryVector), p.dimensions)
	}
	if limit < 1 {
		return nil, fmt.Errorf("vectorstore: limit must be positive")
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('hnsw.iterative_scan', 'strict_order', true)`); err != nil {
		_ = tx.Rollback(ctx)
		rows, qerr := conn.Query(ctx, semanticSQL, vecLiteral(queryVector), workspace, limit)
		if qerr != nil {
			return nil, qerr
		}
		return collect(rows)
	}
	rows, err := tx.Query(ctx, semanticSQL, vecLiteral(queryVector), workspace, limit)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	chunks, err := collect(rows)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return chunks, nil
}

const semanticSQL = `
		SELECT file_path, type, content, metadata::text, outbound::text
		FROM chunks
		WHERE ($2 = '' OR $2 = '*' OR workspace = $2)
		ORDER BY embedding <=> $1::vector
		LIMIT $3`

func (p *Postgres) SearchKeyword(ctx context.Context, keyword string, limit int, workspace string) ([]parser.Chunk, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, fmt.Errorf("vectorstore: empty keyword")
	}
	if limit < 1 {
		return nil, fmt.Errorf("vectorstore: limit must be positive")
	}
	rows, err := p.pool.Query(ctx, `
		SELECT file_path, type, content, metadata::text, outbound::text
		FROM chunks
		WHERE (content ILIKE $1 ESCAPE '\'
		   OR tsv @@ plainto_tsquery('simple', $2))
		  AND ($4 = '' OR $4 = '*' OR workspace = $4)
		ORDER BY (content ILIKE $1 ESCAPE '\') DESC,
		         ts_rank(tsv, plainto_tsquery('simple', $2)) DESC
		LIMIT $3`, likePattern(keyword), keyword, limit, workspace)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

func (p *Postgres) InitExtra(ctx context.Context, slot string, dimensions int) error {
	if !validSlot(slot) {
		return fmt.Errorf("vectorstore: invalid slot name %q", slot)
	}
	if dimensions < 1 || dimensions > 8192 {
		return fmt.Errorf("vectorstore: dimensions %d out of range", dimensions)
	}
	col := "emb_" + slot
	if _, err := p.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE chunks ADD COLUMN IF NOT EXISTS %s vector(%d)`, col, dimensions)); err != nil {
		return err
	}
	var typ string
	err := p.pool.QueryRow(ctx, `SELECT format_type(a.atttypid, a.atttypmod) FROM pg_attribute a
		WHERE a.attrelid = 'chunks'::regclass AND a.attname = $1 AND NOT a.attisdropped`, col).Scan(&typ)
	if err != nil {
		return err
	}
	if width, ok := vectorWidth(typ); ok && width != dimensions {
		return fmt.Errorf("%w: slot %s has %d dimensions, config %d; rebuild the index", ErrDimensionMismatch, slot, width, dimensions)
	}
	if dimensions <= postgresHNSWMaxDim {
		if _, err := p.pool.Exec(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS chunks_%s_idx ON chunks USING hnsw (%s vector_cosine_ops)`, col, col)); err != nil {
			return err
		}
	}
	if p.extra == nil {
		p.extra = map[string]int{}
	}
	p.extra[slot] = dimensions
	return nil
}

func (p *Postgres) SearchExtra(ctx context.Context, slot string, queryVector []float32, limit int, workspace string) ([]parser.Chunk, error) {
	dims, ok := p.extra[slot]
	if !ok {
		return nil, fmt.Errorf("vectorstore: extra slot %q is not initialised", slot)
	}
	if len(queryVector) != dims {
		return nil, fmt.Errorf("vectorstore: vector length %d, want %d", len(queryVector), dims)
	}
	if limit < 1 {
		return nil, fmt.Errorf("vectorstore: limit must be positive")
	}
	col := "emb_" + slot
	rows, err := p.pool.Query(ctx, `
		SELECT file_path, type, content, metadata::text, outbound::text
		FROM chunks
		WHERE `+col+` IS NOT NULL AND ($2 = '' OR $2 = '*' OR workspace = $2)
		ORDER BY `+col+` <=> $1::vector
		LIMIT $3`, vecLiteral(queryVector), workspace, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

func (p *Postgres) ExtraVectorsByFile(ctx context.Context, path, slot string) (map[string][]float32, error) {
	if _, ok := p.extra[slot]; !ok {
		return nil, fmt.Errorf("vectorstore: extra slot %q is not initialised", slot)
	}
	rows, err := p.pool.Query(ctx, `SELECT content, emb_`+slot+`::text FROM chunks WHERE file_path = $1 AND emb_`+slot+` IS NOT NULL`, slashPath(path))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]float32{}
	for rows.Next() {
		var content, lit string
		if err := rows.Scan(&content, &lit); err != nil {
			return nil, err
		}
		v, err := parseVecLiteral(lit)
		if err != nil {
			return nil, err
		}
		out[content] = v
	}
	return out, rows.Err()
}

func (p *Postgres) SearchSummary(ctx context.Context, keyword string, limit int, workspace string) ([]parser.Chunk, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, fmt.Errorf("vectorstore: empty keyword")
	}
	if limit < 1 {
		return nil, fmt.Errorf("vectorstore: limit must be positive")
	}
	rows, err := p.pool.Query(ctx, `
		SELECT file_path, type, content, metadata::text, outbound::text
		FROM chunks
		WHERE coalesce(metadata->>'summary', '') ILIKE $1 ESCAPE '\'
		  AND ($3 = '' OR $3 = '*' OR workspace = $3)
		LIMIT $2`, likePattern(keyword), limit, workspace)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

func (p *Postgres) GetComponentAST(ctx context.Context, name string) (*parser.Chunk, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT file_path, type, content, metadata::text, outbound::text
		FROM chunks
		WHERE metadata->>'name' = $1
		ORDER BY id`, name)
	if err != nil {
		return nil, err
	}
	chunks, err := collect(rows)
	if err != nil {
		return nil, err
	}
	return assembleComponent(chunks)
}

func (p *Postgres) GetComponents(ctx context.Context, name string) ([]parser.Chunk, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT file_path, type, content, metadata::text, outbound::text
		FROM chunks
		WHERE metadata->>'name' = $1
		ORDER BY id`, name)
	if err != nil {
		return nil, err
	}
	chunks, err := collect(rows)
	if err != nil {
		return nil, err
	}
	return assembleComponents(chunks)
}

func (p *Postgres) ListChunks(ctx context.Context) ([]StoredChunk, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, file_path, type, content, metadata::text, outbound::text
		FROM chunks
		ORDER BY id`)
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

func (p *Postgres) ChunksByFiles(ctx context.Context, paths []string) ([]StoredChunk, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	norm := make([]string, len(paths))
	for i, path := range paths {
		norm[i] = slashPath(path)
	}
	rows, err := p.pool.Query(ctx, `
		SELECT id, file_path, type, content, metadata::text, outbound::text
		FROM chunks WHERE file_path = ANY($1) ORDER BY id`, norm)
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

func (p *Postgres) Names(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT DISTINCT metadata->>'name' FROM chunks WHERE coalesce(metadata->>'name', '') <> ''`)
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

func (p *Postgres) UpdateOutbound(ctx context.Context, outbound map[int64][]string) error {
	if len(outbound) == 0 {
		return nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for id, refs := range outbound {
		if _, err := tx.Exec(ctx, `UPDATE chunks SET outbound = $1::jsonb WHERE id = $2`, outboundJSON(refs), id); err != nil {
			return err
		}
	}
	if err := bumpPostgres(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) GetReferences(ctx context.Context, name string) ([]parser.Chunk, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT file_path, type, content, metadata::text, outbound::text
		FROM chunks
		WHERE outbound ? $1
		ORDER BY file_path, id
		LIMIT 100`, name)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

func (p *Postgres) WorkspacesForName(ctx context.Context, name string) ([]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT DISTINCT workspace FROM chunks
		WHERE metadata->>'name' = $1 AND workspace <> ''
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

func (p *Postgres) FileHash(ctx context.Context, path string) (string, bool, error) {
	var hash string
	err := p.pool.QueryRow(ctx, `SELECT content_hash FROM index_state WHERE file_path = $1`, slashPath(path)).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}

func (p *Postgres) VectorsByFile(ctx context.Context, path string) (map[string][]float32, error) {
	rows, err := p.pool.Query(ctx, `SELECT content, embedding::text FROM chunks WHERE file_path = $1`, slashPath(path))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]float32{}
	for rows.Next() {
		var content, lit string
		if err := rows.Scan(&content, &lit); err != nil {
			return nil, err
		}
		v, err := parseVecLiteral(lit)
		if err != nil {
			return nil, err
		}
		out[content] = v
	}
	return out, rows.Err()
}

func (p *Postgres) FileHashes(ctx context.Context) (map[string]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT file_path, content_hash FROM index_state`)
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

func (p *Postgres) Generation(ctx context.Context) (int64, error) {
	var raw string
	err := p.pool.QueryRow(ctx, `SELECT value FROM schema_meta WHERE key = 'index_generation'`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconvParse(raw)
}

func (p *Postgres) Optimize(ctx context.Context) error {
	var hnsw bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'chunks_embedding_idx' AND n.nspname = current_schema()
	)`).Scan(&hnsw); err != nil {
		return err
	}
	if hnsw {
		if _, err := p.pool.Exec(ctx, `REINDEX INDEX CONCURRENTLY chunks_embedding_idx`); err != nil {
			return err
		}
	}
	_, err := p.pool.Exec(ctx, `REINDEX INDEX CONCURRENTLY chunks_tsv_idx`)
	return err
}

func bumpPostgres(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE schema_meta SET value = (value::bigint + 1)::text WHERE key = 'index_generation'`)
	return err
}

func (p *Postgres) Ping(ctx context.Context) error {
	var one int
	return p.pool.QueryRow(ctx, `SELECT 1`).Scan(&one)
}

func (p *Postgres) Close() error {
	p.pool.Close()
	return nil
}

func collect(rows pgx.Rows) ([]parser.Chunk, error) {
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

func (p *Postgres) UnnamedChunks(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `
		SELECT count(*) FROM chunks
		WHERE coalesce(metadata->>'name', '') = ''`).Scan(&n)
	return n, err
}

func (p *Postgres) countBy(ctx context.Context, col string) (map[string]int, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+col+`, count(*) FROM chunks GROUP BY `+col)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		m[key] = n
	}
	return m, rows.Err()
}

func (p *Postgres) Stats(ctx context.Context) (IndexStats, error) {
	var out IndexStats
	if err := p.pool.QueryRow(ctx, `SELECT count(DISTINCT file_path) FROM chunks`).Scan(&out.Files); err != nil {
		return out, err
	}
	var err error
	if out.ByType, err = p.countBy(ctx, "type"); err != nil {
		return out, err
	}
	if out.Workspaces, err = p.countBy(ctx, "workspace"); err != nil {
		return out, err
	}
	for _, n := range out.ByType {
		out.Chunks += n
	}
	out.ByExt = map[string]ExtStat{}
	rows, err := p.pool.Query(ctx, `SELECT file_path, count(*), coalesce(sum(length(content)), 0) FROM chunks GROUP BY file_path`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var file string
		var chunks, bytes int
		if err := rows.Scan(&file, &chunks, &bytes); err != nil {
			return out, err
		}
		addExt(out.ByExt, file, chunks, bytes)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	if err := p.pool.QueryRow(ctx, `SELECT coalesce(max(length(content)), 0), count(*) FILTER (WHERE length(content) > $1) FROM chunks`, LongChunkBytes).
		Scan(&out.MaxChunkBytes, &out.LongChunks); err != nil {
		return out, err
	}
	out.Unnamed, err = p.UnnamedChunks(ctx)
	return out, err
}
