package telemetry

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Event struct {
	Tool            string
	Target          string
	RawBytes        int
	CompressedBytes int
	Rules           int
}

type Summary struct {
	Requests        int
	RawBytes        int
	CompressedBytes int
	Rules           int
}

type Log struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS telemetry_events (
	timestamp TEXT NOT NULL,
	tool_name TEXT NOT NULL,
	target TEXT NOT NULL,
	raw_bytes INTEGER NOT NULL,
	compressed_bytes INTEGER NOT NULL,
	rules_injected_count INTEGER NOT NULL
)`

func Open(path string) (*Log, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Log{db: db}, nil
}

func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	return l.db.Close()
}

func (l *Log) Record(ctx context.Context, e Event) {
	if l == nil {
		return
	}
	_, _ = l.db.ExecContext(ctx, `INSERT INTO telemetry_events VALUES (?,?,?,?,?,?)`,
		time.Now().UTC().Format(time.RFC3339), e.Tool, e.Target, e.RawBytes, e.CompressedBytes, e.Rules)
}

func (l *Log) Summary(ctx context.Context) (Summary, error) {
	var s Summary
	if l == nil {
		return s, nil
	}
	err := l.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(raw_bytes),0), COALESCE(SUM(compressed_bytes),0), COALESCE(SUM(rules_injected_count),0) FROM telemetry_events`).
		Scan(&s.Requests, &s.RawBytes, &s.CompressedBytes, &s.Rules)
	return s, err
}

// Saved estimates the tokens kept out of the model's context, at four bytes each.
func (s Summary) Saved() int {
	if s.RawBytes <= s.CompressedBytes {
		return 0
	}
	return (s.RawBytes - s.CompressedBytes) / 4
}

// Ratio is the share of raw bytes that did not reach the model.
func (s Summary) Ratio() float64 {
	if s.RawBytes == 0 || s.RawBytes <= s.CompressedBytes {
		return 0
	}
	return float64(s.RawBytes-s.CompressedBytes) / float64(s.RawBytes)
}

// ToolStat is what one tool, or one target of a tool, accounts for.
type ToolStat struct {
	Tool            string
	Target          string
	Requests        int
	RawBytes        int
	CompressedBytes int
}

// Saved is the tokens kept out of the context at four bytes each, never negative.
func (t ToolStat) Saved() int {
	if t.RawBytes <= t.CompressedBytes {
		return 0
	}
	return (t.RawBytes - t.CompressedBytes) / 4
}

// ByTarget totals the events since a time per tool and target.
func (l *Log) ByTarget(ctx context.Context, since time.Time) ([]ToolStat, error) {
	return l.stats(ctx, since, `SELECT tool_name, target, COUNT(*), COALESCE(SUM(raw_bytes),0), COALESCE(SUM(compressed_bytes),0)
		FROM telemetry_events WHERE timestamp >= ? GROUP BY tool_name, target ORDER BY SUM(raw_bytes) DESC`)
}

func (l *Log) stats(ctx context.Context, since time.Time, query string) ([]ToolStat, error) {
	if l == nil {
		return nil, nil
	}
	from := ""
	if !since.IsZero() {
		from = since.UTC().Format(time.RFC3339)
	}
	rows, err := l.db.QueryContext(ctx, query, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolStat
	for rows.Next() {
		var s ToolStat
		if err := rows.Scan(&s.Tool, &s.Target, &s.Requests, &s.RawBytes, &s.CompressedBytes); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
