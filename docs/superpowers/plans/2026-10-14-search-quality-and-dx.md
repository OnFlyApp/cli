# Search quality, code intelligence, cost and DX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an eval harness, a text fallback parser, an optional reranker, call-graph tools, overload disambiguation, per-file vector reuse, `serve --watch`, and the `search`, `stats`, `eval`, `mcp-config` commands.

**Architecture:** The search pipeline (embed, semantic, keyword, fuse) moves out of `internal/mcp` into a new `internal/query` package with one `Searcher`. The MCP server, `onfly search` and `onfly eval` all call it, so tuning happens in one place. Everything else is additive: new Store methods, new parser type `text`, new MCP tools, new cobra commands. `schemaVersion` does not change.

**Tech Stack:** Go 1.27, `CGO_ENABLED=0`, `modernc.org/sqlite` with sqlite-vec, pgx/pgvector, cobra, `go.yaml.in/yaml/v3`, MCP go-sdk.

**Spec:** This file. Scope comes from the 2026-10-14 improvement review: search quality (1-3), code intelligence (4-5), cost and speed (6, 8), developer experience (9, 10). Item 7 (embedder retry and batching) is already in the repo (`internal/embedder/embedder.go` `postJSON`, `internal/index/index.go` `embedChunks`), so it has no task.

## Global Constraints

- `CGO_ENABLED=0` for build and test. Postgres tests skip unless `ONFLY_TEST_POSTGRES_DSN` is set. Do not start Docker.
- `schemaVersion` stays `"2"`. No new tables or columns.
- Existing MCP tool names and their input fields stay. New fields are optional.
- No new dependencies. Use stdlib, cobra, `go.yaml.in/yaml/v3`, and what `go.mod` already lists.
- Do not vendor a parser grammar.
- `docs/` is updated in the same task as the behavior it describes.
- Every task ends green: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./... -count=1 -timeout 300s -skip TestE2EGoParser`.
- Commits use explicit paths. Commit messages end with the attribution line from the session reminder.

## Review Focus

- A keyword or FTS failure must not drop semantic hits through `Searcher`. Task 1 tests a quoted query.
- `onfly search` with no index or no match exits 0 with empty output for no match, and a non-zero status with the store error when the database cannot open. Task 2 tests the formatter on empty input.
- A reranker error or a malformed reranker response must fall back to fused order, never fail the search. Task 7 tests this.
- Reranker index values outside the candidate range must be ignored. Task 7 tests this.
- `get_impact` must terminate on a reference cycle (`A` calls `B`, `B` calls `A`). Task 8 tests this.
- `get_component_ast` without `receiver` on a name shared by two receivers returns one chunk plus the other receivers as `alternatives`, and does not hide them. Task 9 tests this.
- A vector reused from a previous chunk must have the configured dimension, or the chunk is re-embedded. Task 10 tests this.
- A changed embedding model with the same dimensions is not detected by reuse. This matches the existing hash-skip behavior and is documented in Task 10.
- `serve --watch` must stop the watcher when the server context ends, and a watcher failure must not kill the MCP session. Task 11 tests the shutdown.
- A `text` parser file with only whitespace returns `[]`, so the file's rows are deleted like any other empty parse. Task 6 tests this.

---

### Task 1: Extract the search pipeline into `internal/query`

**Files:**

- Create: `internal/query/query.go`
- Test: `internal/query/query_test.go`
- Modify: `internal/mcp/server.go` (`tools` struct, `NewServer`, `finishSearch` near line 282, `scope` near line 569, `clampLimit` near line 714, `candidatePool` near line 724, `fuseChunks` and `chunkKey` near line 783)
- Modify: `internal/mcp/server_test.go` (only if it references `fuseChunks`, `chunkKey` or `candidatePool`)

**Interfaces:**

- Produces: `query.Searcher{Store vectorstore.Store; Emb embedder.Embedder; Logger *slog.Logger}`, `func (s *Searcher) Search(ctx context.Context, q string, limit int, workspace string) ([]parser.Chunk, error)`, `func Open(ctx context.Context, cfg config.Config) (*Searcher, func() error, error)`, `func Fuse(lists ...[]parser.Chunk) []parser.Chunk`, `func ClampLimit(n int) int`, `func CandidatePool(limit int) int`, `func Scope(def, arg string) string`
- Consumes: `Store.SearchSemantic`, `Store.SearchKeyword`, `embedder.New`, `vectorstore.New`

- [ ] **Step 1: Write the failing test**

```go
package query

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

type vecEmb struct{ v []float32 }

func (e vecEmb) Embed(context.Context, string) ([]float32, error) { return e.v, nil }
func (e vecEmb) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = e.v
	}
	return out, nil
}

func newSearcher(t *testing.T) *Searcher {
	t.Helper()
	st, err := vectorstore.OpenSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	exact := parser.Chunk{FilePath: "a.go", Type: "function", Content: "func getUserId() {}", Metadata: map[string]string{"name": "getUserId"}}
	near := parser.Chunk{FilePath: "b.go", Type: "function", Content: "func other() {}", Metadata: map[string]string{"name": "other"}}
	if err := storetest.Save(ctx, st, []vectorstore.EmbeddedChunk{
		{Chunk: exact, Vector: []float32{0, 1, 0, 0}},
		{Chunk: near, Vector: []float32{1, 0, 0, 0}},
	}); err != nil {
		t.Fatal(err)
	}
	return &Searcher{Store: st, Emb: vecEmb{v: []float32{1, 0, 0, 0}}}
}

func TestSearchFusesBothRankings(t *testing.T) {
	got, err := newSearcher(t).Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "a.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSearchSurvivesBadKeywordQuery(t *testing.T) {
	got, err := newSearcher(t).Search(context.Background(), `getUserId "quote`, 5, "")
	if err != nil || len(got) == 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestScope(t *testing.T) {
	if Scope("ws", "") != "ws" || Scope("ws", "*") != "" || Scope("ws", "other") != "other" {
		t.Fatal("scope")
	}
}
```

- [ ] **Step 2: Run the test**

Run: `CGO_ENABLED=0 go test ./internal/query/ -count=1`

Expected: FAIL, package has no Go files or `Searcher` undefined.

- [ ] **Step 3: Implement the package**

```go
// Package query runs one search: embed, semantic, keyword, reciprocal-rank fusion.
package query

import (
	"context"
	"io"
	"log/slog"
	"sort"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

const (
	DefaultLimit = 10
	MaxLimit     = 50
	keywordPool  = 20
	rrfK         = 60
)

func ClampLimit(n int) int {
	if n < 1 {
		return DefaultLimit
	}
	if n > MaxLimit {
		return MaxLimit
	}
	return n
}

func CandidatePool(limit int) int {
	return max(ClampLimit(limit), keywordPool)
}

// Scope maps a tool argument to a store workspace: "" is the default, "*" is all.
func Scope(def, arg string) string {
	switch arg {
	case "":
		return def
	case "*":
		return ""
	}
	return arg
}

type Searcher struct {
	Store  vectorstore.Store
	Emb    embedder.Embedder
	Logger *slog.Logger
}

func (s *Searcher) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Logger
}

func (s *Searcher) Search(ctx context.Context, q string, limit int, workspace string) ([]parser.Chunk, error) {
	vec, err := s.Emb.Embed(ctx, q)
	if err != nil {
		return nil, err
	}
	pool := CandidatePool(limit)
	semantic, err := s.Store.SearchSemantic(ctx, vec, pool, workspace)
	if err != nil {
		return nil, err
	}
	keyword, err := s.Store.SearchKeyword(ctx, q, pool, workspace)
	if err != nil {
		s.logger().Warn("search keyword", "err", err)
		keyword = nil
	}
	fused := Fuse(semantic, keyword)
	if n := ClampLimit(limit); len(fused) > n {
		fused = fused[:n]
	}
	return fused, nil
}

// Open builds a Searcher for command-line use. Close the store with the returned func.
func Open(ctx context.Context, cfg config.Config) (*Searcher, func() error, error) {
	store, err := vectorstore.New(ctx, cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	if err := store.Init(ctx, cfg.Embedder.Dimensions); err != nil {
		store.Close()
		return nil, nil, err
	}
	emb, err := embedder.New(cfg.Embedder)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return &Searcher{Store: store, Emb: emb}, store.Close, nil
}

// Fuse merges ranked lists by reciprocal rank. A chunk in several lists
// appears once and outranks a chunk that is first in only one list.
func Fuse(lists ...[]parser.Chunk) []parser.Chunk {
	type acc struct {
		chunk parser.Chunk
		score float64
	}
	var order []string
	by := map[string]*acc{}
	for _, list := range lists {
		seen := map[string]bool{}
		rank := 0
		for _, ch := range list {
			key := chunkKey(ch)
			if seen[key] {
				continue
			}
			seen[key] = true
			rank++
			item := by[key]
			if item == nil {
				item = &acc{chunk: ch}
				by[key] = item
				order = append(order, key)
			}
			item.score += 1 / float64(rrfK+rank)
		}
	}
	out := make([]parser.Chunk, 0, len(order))
	for _, key := range order {
		out = append(out, by[key].chunk)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := by[chunkKey(out[i])].score, by[chunkKey(out[j])].score
		if si != sj {
			return si > sj
		}
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		return out[i].Content < out[j].Content
	})
	return out
}

// chunkKey identifies a chunk across result lists. Path, type, name, part,
// first line and content together are unique per row.
func chunkKey(c parser.Chunk) string {
	m := c.Metadata
	return c.FilePath + "\x00" + c.Type + "\x00" + m["name"] + "\x00" + m["part"] + "\x00" + m["start_line"] + "\x00" + c.Content
}
```

Check that `vectorstore.OpenSQLite` and `storetest.Save` are the exported names (`grep -n "func OpenSQLite" internal/vectorstore/sqlite.go`). The tests in `sqlite_test.go` use `OpenSQLite`, so they exist.

- [ ] **Step 4: Run the package tests**

Run: `CGO_ENABLED=0 go test ./internal/query/ -count=1`

Expected: PASS.

- [ ] **Step 5: Switch the MCP server to the Searcher**

In `internal/mcp/server.go`:

1. Add `searcher *query.Searcher` to `tools`. In `NewServer`, after `h := &tools{...}`, set `h.searcher = &query.Searcher{Store: store, Emb: emb, Logger: logger}`.
2. Replace the body of `finishSearch` up to and including `fused = fused[:limit]` with:

```go
fused, err := h.searcher.Search(ctx, in.Query, in.Limit, workspace)
if err != nil {
	return nil, hitList{}, err
}
```

   The lines after it (`budget, text := h.fitBudget(...)`, `fitHits`, `record`) stay.
3. Make `(*tools).scope` return `query.Scope(h.workspace, arg)`.
4. Make `clampLimit` return `query.ClampLimit(n)`. Delete `candidatePool`, `fuseChunks` and `chunkKey`. Delete `defaultSemLimit` and `maxSemLimit` if the compiler reports them unused. Keep `keywordLimit`, which `searchKeyword` uses.
5. Add `"github.com/axitdev/onfly/internal/query"` to the imports and drop `sort` if it is now unused.

If `server_test.go` has tests that call `fuseChunks` or `candidatePool`, move them into `internal/query/query_test.go` against `Fuse` and `CandidatePool`.

- [ ] **Step 6: Run everything**

Run: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./internal/... ./cmd/ -count=1 -timeout 300s -skip TestE2EGoParser`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/query/query.go internal/query/query_test.go internal/mcp/server.go internal/mcp/server_test.go
git commit -m "Move the search pipeline into internal/query."
```

---

### Task 2: `onfly search`

**Files:**

- Create: `cmd/search.go`
- Test: `cmd/search_test.go`
- Modify: `cmd/root.go` (`AddCommand` list)
- Modify: `README.md`, `docs/development.md` (one short section)

**Interfaces:**

- Consumes: `query.Open`, `query.Scope`, `Searcher.Search`
- Produces: command `onfly search <query...> [--limit N] [--workspace W] [--json]`; `func printHits(w io.Writer, chunks []parser.Chunk, asJSON bool) error`

- [ ] **Step 1: Write the failing test**

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func TestPrintHitsPlain(t *testing.T) {
	var b bytes.Buffer
	err := printHits(&b, []parser.Chunk{
		{FilePath: "a.go", Content: "x", Metadata: map[string]string{"name": "Save", "receiver": "User", "start_line": "12", "end_line": "30"}},
		{FilePath: "b.go", Content: "y"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "a.go:12-30  User.Save\nb.go\n"
	if b.String() != want {
		t.Fatalf("%q", b.String())
	}
}

func TestPrintHitsJSONAndEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := printHits(&b, nil, true); err != nil || strings.TrimSpace(b.String()) != "[]" {
		t.Fatalf("%q %v", b.String(), err)
	}
	b.Reset()
	if err := printHits(&b, nil, false); err != nil || b.Len() != 0 {
		t.Fatalf("%q %v", b.String(), err)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `CGO_ENABLED=0 go test ./cmd/ -run TestPrintHits -count=1`

Expected: FAIL, `printHits` undefined.

- [ ] **Step 3: Implement**

```go
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	"github.com/spf13/cobra"
)

var (
	searchLimit     int
	searchWorkspace string
	searchJSON      bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search the index from the terminal",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		s, closeStore, err := query.Open(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		defer closeStore()
		chunks, err := s.Search(cmd.Context(), strings.Join(args, " "), searchLimit, query.Scope(cfg.Workspace, searchWorkspace))
		if err != nil {
			return err
		}
		return printHits(cmd.OutOrStdout(), chunks, searchJSON)
	},
}

func init() {
	searchCmd.Flags().IntVar(&searchLimit, "limit", query.DefaultLimit, "maximum results, from 1 to 50")
	searchCmd.Flags().StringVar(&searchWorkspace, "workspace", "", "workspace name, or * for every workspace")
	searchCmd.Flags().BoolVar(&searchJSON, "json", false, "print JSON with chunk content")
}

type hitRow struct {
	FilePath  string `json:"file_path"`
	Symbol    string `json:"symbol,omitempty"`
	Receiver  string `json:"receiver,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Content   string `json:"content"`
}

func rowOf(c parser.Chunk) hitRow {
	row := hitRow{FilePath: c.FilePath, Content: c.Content}
	row.Symbol = c.Metadata["name"]
	row.Receiver = c.Metadata["receiver"]
	row.StartLine, _ = strconv.Atoi(c.Metadata["start_line"])
	row.EndLine, _ = strconv.Atoi(c.Metadata["end_line"])
	return row
}

func printHits(w io.Writer, chunks []parser.Chunk, asJSON bool) error {
	rows := make([]hitRow, 0, len(chunks))
	for _, c := range chunks {
		rows = append(rows, rowOf(c))
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	for _, r := range rows {
		line := r.FilePath
		if r.StartLine > 0 {
			line += fmt.Sprintf(":%d-%d", r.StartLine, max(r.EndLine, r.StartLine))
		}
		if sym := strings.TrimPrefix(r.Receiver+"."+r.Symbol, "."); r.Symbol != "" {
			line += "  " + sym
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
```

Add `searchCmd` to the `rootCmd.AddCommand(...)` list in `cmd/root.go`. A symbol with no receiver prints as `Name` because of the `TrimPrefix`.

- [ ] **Step 4: Run the tests**

Run: `CGO_ENABLED=0 go test ./cmd/ -run TestPrintHits -count=1`

Expected: PASS.

- [ ] **Step 5: Document**

`README.md`: after the index section, add `onfly search "where is the file hash computed"` with the sample output `internal/index/index.go:62-64  ContentHash`. `docs/development.md`: one sentence that `search` uses the same pipeline as the MCP `search` tool.

- [ ] **Step 6: Commit**

```bash
git add cmd/search.go cmd/search_test.go cmd/root.go README.md docs/development.md
git commit -m "Add onfly search for terminal queries."
```

---

### Task 3: `Stats` and `onfly stats`

**Files:**

- Modify: `internal/vectorstore/factory.go` (`Checker` interface)
- Modify: `internal/vectorstore/store.go` (`IndexStats` type)
- Modify: `internal/vectorstore/sqlite.go`, `internal/vectorstore/postgres.go` (append `Stats`)
- Test: `internal/vectorstore/sqlite_test.go`, `cmd/stats_test.go`
- Create: `cmd/stats.go`
- Modify: `cmd/root.go`

**Interfaces:**

- Consumes: `UnnamedChunks`, `OpenChecker`, `CheckMeta`
- Produces: `type IndexStats struct{ Files, Chunks, Unnamed int; ByType, Workspaces map[string]int }`, `Stats(ctx context.Context) (IndexStats, error)` on `Checker`, `*SQLite`, `*Postgres`; command `onfly stats`; `func formatStats(w io.Writer, s vectorstore.IndexStats)`

- [ ] **Step 1: Write the failing tests**

In `sqlite_test.go`:

```go
func TestSQLiteStats(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	vec := []float32{1, 0, 0, 0}
	fn := parser.Chunk{FilePath: "a.go", Type: "function", Content: "func A() {}", Metadata: map[string]string{"name": "A"}}
	blank := parser.Chunk{FilePath: "b.go", Type: "file", Content: "package p", Metadata: map[string]string{}}
	if err := st.CommitFile(ctx, "a.go", "h1", []EmbeddedChunk{{Chunk: fn, Vector: vec, Workspace: "backend"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CommitFile(ctx, "b.go", "h2", []EmbeddedChunk{{Chunk: blank, Vector: vec, Workspace: "backend"}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Files != 2 || got.Chunks != 2 || got.Unnamed != 1 || got.ByType["function"] != 1 || got.Workspaces["backend"] != 2 {
		t.Fatalf("%+v", got)
	}
}
```

In `cmd/stats_test.go`:

```go
package cmd

import (
	"bytes"
	"testing"

	"github.com/axitdev/onfly/internal/vectorstore"
)

func TestFormatStats(t *testing.T) {
	var b bytes.Buffer
	formatStats(&b, vectorstore.IndexStats{
		Files: 2, Chunks: 3, Unnamed: 1,
		ByType:     map[string]int{"method": 1, "function": 2},
		Workspaces: map[string]int{"backend": 3},
	})
	want := "files: 2\nchunks: 3 (1 without metadata.name)\ntypes: function=2 method=1\nworkspaces: backend=3\n"
	if b.String() != want {
		t.Fatalf("%q", b.String())
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/vectorstore/ ./cmd/ -run 'TestSQLiteStats|TestFormatStats' -count=1`

Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`store.go`:

```go
// IndexStats summarizes what an index holds.
type IndexStats struct {
	Files      int
	Chunks     int
	Unnamed    int
	ByType     map[string]int
	Workspaces map[string]int
}
```

Add `Stats(ctx context.Context) (IndexStats, error)` to the `Checker` interface in `factory.go`.

`sqlite.go` (append):

```go
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
```

`postgres.go` (append): same shape. Use `p.pool.QueryRow(ctx, ...)` and `p.pool.Query(ctx, ...)` on table `chunks`, columns `file_path`, `type`, `workspace`. Pgx rows: `defer rows.Close()` inside a helper `func (p *Postgres) countBy(ctx context.Context, col string) (map[string]int, error)` taking a fixed column name (never user input), then `Stats` calls it for `"type"` and `"workspace"`.

`cmd/stats.go`:

```go
package cmd

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Print index size and chunk breakdown",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		st, err := vectorstore.OpenChecker(cmd.Context(), cfg.Database)
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.CheckMeta(cmd.Context(), cfg.Embedder.Dimensions); err != nil {
			return err
		}
		s, err := st.Stats(cmd.Context())
		if err != nil {
			return err
		}
		formatStats(cmd.OutOrStdout(), s)
		return nil
	},
}

func formatStats(w io.Writer, s vectorstore.IndexStats) {
	fmt.Fprintf(w, "files: %d\n", s.Files)
	if s.Unnamed > 0 {
		fmt.Fprintf(w, "chunks: %d (%d without metadata.name)\n", s.Chunks, s.Unnamed)
	} else {
		fmt.Fprintf(w, "chunks: %d\n", s.Chunks)
	}
	fmt.Fprintf(w, "types: %s\n", joinCounts(s.ByType))
	if named := withoutEmpty(s.Workspaces); len(named) > 0 {
		fmt.Fprintf(w, "workspaces: %s\n", joinCounts(named))
	}
}

func withoutEmpty(m map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		if k != "" {
			out[k] = v
		}
	}
	return out
}

func joinCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}
```

Add `statsCmd` to `rootCmd.AddCommand`.

- [ ] **Step 4: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/vectorstore/ ./cmd/ -count=1 -timeout 180s -skip TestE2EGoParser`

Expected: PASS. The Postgres contract test skips without a DSN.

- [ ] **Step 5: Commit**

```bash
git add internal/vectorstore/factory.go internal/vectorstore/store.go internal/vectorstore/sqlite.go internal/vectorstore/postgres.go internal/vectorstore/sqlite_test.go cmd/stats.go cmd/stats_test.go cmd/root.go
git commit -m "Add onfly stats."
```

---

### Task 4: `onfly mcp-config`

**Files:**

- Create: `cmd/mcpconfig.go`
- Test: `cmd/mcpconfig_test.go`
- Modify: `cmd/root.go`, `README.md`

**Interfaces:**

- Produces: command `onfly mcp-config <claude-code|cursor|vscode>`; `func mcpSnippet(client string, serveArgs []string) (file string, body []byte, err error)`

Zed is left out on purpose: its MCP settings shape has changed between releases and a wrong snippet is worse than none. Add it when someone checks the current format.

- [ ] **Step 1: Write the failing test**

```go
package cmd

import (
	"encoding/json"
	"testing"
)

func TestMCPSnippet(t *testing.T) {
	for client, wantFile := range map[string]string{
		"claude-code": ".mcp.json",
		"cursor":      ".cursor/mcp.json",
		"vscode":      ".vscode/mcp.json",
	} {
		file, body, err := mcpSnippet(client, []string{"serve", "--config", "/p/.onfly/config.yaml"})
		if err != nil || file != wantFile {
			t.Fatalf("%s: %q %v", client, file, err)
		}
		var v map[string]map[string]map[string]any
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("%s: %v", client, err)
		}
		var entry map[string]any
		for _, servers := range v {
			entry = servers["onfly"]
		}
		args, _ := entry["args"].([]any)
		if entry["command"] != "onfly" || len(args) != 3 || args[0] != "serve" {
			t.Fatalf("%s: %+v", client, entry)
		}
	}
	if _, _, err := mcpSnippet("emacs", nil); err == nil {
		t.Fatal("unknown client must fail")
	}
}
```

- [ ] **Step 2: Run the test**

Run: `CGO_ENABLED=0 go test ./cmd/ -run TestMCPSnippet -count=1`

Expected: FAIL, `mcpSnippet` undefined.

- [ ] **Step 3: Implement**

```go
package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

var mcpConfigCmd = &cobra.Command{
	Use:       "mcp-config <claude-code|cursor|vscode>",
	Short:     "Print the MCP server entry for an IDE",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"claude-code", "cursor", "vscode"},
	RunE: func(cmd *cobra.Command, args []string) error {
		serveArgs := []string{"serve"}
		if configPath != "" {
			abs, err := filepath.Abs(configPath)
			if err != nil {
				return err
			}
			serveArgs = append(serveArgs, "--config", abs)
		}
		file, body, err := mcpSnippet(args[0], serveArgs)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Add this to %s:\n", file)
		_, err = cmd.OutOrStdout().Write(append(body, '\n'))
		return err
	},
}

func mcpSnippet(client string, serveArgs []string) (string, []byte, error) {
	entry := map[string]any{"command": "onfly", "args": serveArgs}
	var file string
	var doc map[string]any
	switch client {
	case "claude-code":
		file, doc = ".mcp.json", map[string]any{"mcpServers": map[string]any{"onfly": entry}}
	case "cursor":
		file, doc = ".cursor/mcp.json", map[string]any{"mcpServers": map[string]any{"onfly": entry}}
	case "vscode":
		entry["type"] = "stdio"
		file, doc = ".vscode/mcp.json", map[string]any{"servers": map[string]any{"onfly": entry}}
	default:
		return "", nil, fmt.Errorf("unknown client %q: use claude-code, cursor or vscode", client)
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	return file, body, err
}
```

Add `mcpConfigCmd` to `rootCmd.AddCommand`. The JSON goes to stdout and the file hint to stderr, so `onfly mcp-config cursor > .cursor/mcp.json` works.

- [ ] **Step 4: Run the test**

Run: `CGO_ENABLED=0 go test ./cmd/ -run TestMCPSnippet -count=1`

Expected: PASS.

- [ ] **Step 5: Document**

`README.md`: add a short "Connect an IDE" section with `onfly mcp-config cursor`.

- [ ] **Step 6: Commit**

```bash
git add cmd/mcpconfig.go cmd/mcpconfig_test.go cmd/root.go README.md
git commit -m "Add onfly mcp-config for IDE setup."
```

---

### Task 5: Eval harness and `onfly eval`

**Files:**

- Create: `internal/eval/eval.go`
- Test: `internal/eval/eval_test.go`
- Create: `cmd/eval.go`
- Modify: `cmd/root.go`
- Create: `docs/evaluation.md`
- Modify: `README.md` (link)

**Interfaces:**

- Consumes: `query.Open`, `Searcher.Search`
- Produces: `eval.Load(path string) ([]eval.Case, error)`, `eval.Run(ctx, cases, search SearchFunc, k int) (eval.Report, error)`, `type SearchFunc func(ctx context.Context, q string, limit int) ([]parser.Chunk, error)`; command `onfly eval [--cases .onfly/eval.yaml] [--k 10] [--min-recall 0] [--json]`

Case file:

```yaml
cases:
  - query: where is the content hash computed
    expect:
      - file: internal/index/index.go
        symbol: ContentHash
```

A case passes at rank r when any returned chunk has a path ending in `expect.file` and, if `symbol` is set, `metadata.name == symbol`. Recall@k is the share of cases with a hit in the top k. MRR averages `1/rank` of the first hit (0 for a miss).

- [ ] **Step 1: Write the failing test**

```go
package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func chunk(file, name string) parser.Chunk {
	return parser.Chunk{FilePath: file, Metadata: map[string]string{"name": name}}
}

func TestRunScoresRecallAndMRR(t *testing.T) {
	cases := []Case{
		{Query: "hash", Expect: []Expect{{File: "index.go", Symbol: "ContentHash"}}},
		{Query: "walk", Expect: []Expect{{File: "walk.go"}}},
		{Query: "nothing", Expect: []Expect{{File: "missing.go"}}},
	}
	search := func(_ context.Context, q string, _ int) ([]parser.Chunk, error) {
		switch q {
		case "hash":
			return []parser.Chunk{chunk("a.go", "A"), chunk("internal/index/index.go", "ContentHash")}, nil
		case "walk":
			return []parser.Chunk{chunk("internal/index/walk.go", "walk")}, nil
		}
		return []parser.Chunk{chunk("a.go", "A")}, nil
	}
	rep, err := Run(context.Background(), cases, search, 10)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Hits != 2 || rep.Total != 3 {
		t.Fatalf("%+v", rep)
	}
	wantMRR := (0.5 + 1.0 + 0) / 3
	if d := rep.MRR - wantMRR; d > 1e-9 || d < -1e-9 {
		t.Fatalf("mrr %v want %v", rep.MRR, wantMRR)
	}
	if rep.Cases[0].Rank != 2 || rep.Cases[2].Rank != 0 {
		t.Fatalf("%+v", rep.Cases)
	}
}

func TestSymbolMustMatchWhenSet(t *testing.T) {
	cases := []Case{{Query: "q", Expect: []Expect{{File: "a.go", Symbol: "Want"}}}}
	search := func(context.Context, string, int) ([]parser.Chunk, error) {
		return []parser.Chunk{chunk("a.go", "Other")}, nil
	}
	rep, _ := Run(context.Background(), cases, search, 10)
	if rep.Hits != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestLoadRejectsEmptyExpectation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eval.yaml")
	os.WriteFile(path, []byte("cases:\n  - query: x\n    expect: []\n"), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("a case without expectations must fail to load")
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/eval/ -count=1`

Expected: FAIL, package missing.

- [ ] **Step 3: Implement the package**

```go
// Package eval scores search results against hand-written expectations.
package eval

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"go.yaml.in/yaml/v3"
)

type Expect struct {
	File   string `yaml:"file" json:"file"`
	Symbol string `yaml:"symbol,omitempty" json:"symbol,omitempty"`
}

type Case struct {
	Query  string   `yaml:"query" json:"query"`
	Expect []Expect `yaml:"expect" json:"expect"`
}

type CaseResult struct {
	Query string `json:"query"`
	Rank  int    `json:"rank"` // 1-based; 0 is a miss
}

type Report struct {
	K      int          `json:"k"`
	Total  int          `json:"total"`
	Hits   int          `json:"hits"`
	Recall float64      `json:"recall"`
	MRR    float64      `json:"mrr"`
	Cases  []CaseResult `json:"cases"`
}

type SearchFunc func(ctx context.Context, q string, limit int) ([]parser.Chunk, error)

func Load(path string) ([]Case, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Cases []Case `yaml:"cases"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Cases) == 0 {
		return nil, fmt.Errorf("%s: no cases", path)
	}
	for i, c := range doc.Cases {
		if strings.TrimSpace(c.Query) == "" || len(c.Expect) == 0 {
			return nil, fmt.Errorf("%s: case %d needs a query and at least one expectation", path, i+1)
		}
		for _, e := range c.Expect {
			if e.File == "" {
				return nil, fmt.Errorf("%s: case %d has an expectation without a file", path, i+1)
			}
		}
	}
	return doc.Cases, nil
}

func matches(c parser.Chunk, want Expect) bool {
	if !strings.HasSuffix(c.FilePath, want.File) {
		return false
	}
	return want.Symbol == "" || c.Metadata["name"] == want.Symbol
}

func Run(ctx context.Context, cases []Case, search SearchFunc, k int) (Report, error) {
	rep := Report{K: k, Total: len(cases)}
	var mrr float64
	for _, c := range cases {
		got, err := search(ctx, c.Query, k)
		if err != nil {
			return rep, fmt.Errorf("query %q: %w", c.Query, err)
		}
		rank := 0
	scan:
		for i, ch := range got {
			if i >= k {
				break
			}
			for _, want := range c.Expect {
				if matches(ch, want) {
					rank = i + 1
					break scan
				}
			}
		}
		if rank > 0 {
			rep.Hits++
			mrr += 1 / float64(rank)
		}
		rep.Cases = append(rep.Cases, CaseResult{Query: c.Query, Rank: rank})
	}
	if rep.Total > 0 {
		rep.Recall = float64(rep.Hits) / float64(rep.Total)
		rep.MRR = mrr / float64(rep.Total)
	}
	return rep, nil
}
```

- [ ] **Step 4: Run the package tests**

Run: `CGO_ENABLED=0 go test ./internal/eval/ -count=1`

Expected: PASS.

- [ ] **Step 5: Add the command**

```go
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/axitdev/onfly/internal/eval"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	"github.com/spf13/cobra"
)

var (
	evalCases     string
	evalK         int
	evalMinRecall float64
	evalJSON      bool
)

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Score search results against expected files and symbols",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		path := evalCases
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.ProjectRoot, path)
		}
		cases, err := eval.Load(path)
		if err != nil {
			return err
		}
		s, closeStore, err := query.Open(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		defer closeStore()
		ws := query.Scope(cfg.Workspace, "")
		rep, err := eval.Run(cmd.Context(), cases, func(ctx context.Context, q string, limit int) ([]parser.Chunk, error) {
			return s.Search(ctx, q, limit, ws)
		}, evalK)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if evalJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(rep); err != nil {
				return err
			}
		} else {
			for _, c := range rep.Cases {
				mark := fmt.Sprintf("rank %d", c.Rank)
				if c.Rank == 0 {
					mark = "miss"
				}
				fmt.Fprintf(out, "%-8s %s\n", mark, c.Query)
			}
			fmt.Fprintf(out, "recall@%d: %.2f (%d/%d)  mrr: %.3f\n", rep.K, rep.Recall, rep.Hits, rep.Total, rep.MRR)
		}
		if rep.Recall < evalMinRecall {
			return fmt.Errorf("recall %.2f is below --min-recall %.2f", rep.Recall, evalMinRecall)
		}
		return nil
	},
}

func init() {
	evalCmd.Flags().StringVar(&evalCases, "cases", ".onfly/eval.yaml", "path to the cases file, relative to the project root")
	evalCmd.Flags().IntVar(&evalK, "k", 10, "rank cutoff")
	evalCmd.Flags().Float64Var(&evalMinRecall, "min-recall", 0, "exit non-zero when recall is below this value")
	evalCmd.Flags().BoolVar(&evalJSON, "json", false, "print the report as JSON")
}
```

Add `evalCmd` to `rootCmd.AddCommand`.

- [ ] **Step 6: Document**

`docs/evaluation.md`: the case file format above, what recall@k and MRR mean, the advice to write 20 to 50 real questions, and a before/after workflow (`onfly eval --json > before.json`, change a setting, rerun, compare). Link it from `README.md`.

- [ ] **Step 7: Run and commit**

Run: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./internal/eval/ ./cmd/ -count=1 -timeout 180s -skip TestE2EGoParser`

Expected: PASS.

```bash
git add internal/eval/eval.go internal/eval/eval_test.go cmd/eval.go cmd/root.go docs/evaluation.md README.md
git commit -m "Add an eval harness for search quality."
```

---

### Task 6: `text` fallback parser

**Files:**

- Create: `internal/parser/text.go`
- Test: `internal/parser/text_test.go`
- Modify: `internal/parser/router.go` (`openParser`)
- Modify: `internal/config/config.go` (parser type switch near line 518)
- Modify: `cmd/doctor.go` (`checkPlugins` switch near line 111)
- Modify: `docs/parsers.md`

**Interfaces:**

- Produces: `type TextParser struct{}` implementing `Parser`; config `parsers: [{type: text, extensions: [".md", ".yaml"]}]`

Behavior: Markdown files (`.md`, `.markdown`, `.mdx`) split on ATX headings. Each section is a chunk of type `section` with `metadata.name = "<base>#<heading>"`. Text before the first heading is named `<base>`. Every other extension splits into runs of blank-line-separated paragraphs of at least 30 lines each, type `block`, `metadata.name = "<base>:<start line>"`. Both set `start_line` and `end_line`. Names carry the base file name so two `README.md` files do not share a bare heading name.

- [ ] **Step 1: Write the failing tests**

```go
package parser

import (
	"context"
	"strings"
	"testing"
)

func TestTextParserMarkdownSections(t *testing.T) {
	src := "intro line\n\n# Install\nrun it\n\n## Flags\n--x\n"
	got, err := TextParser{}.Parse(context.Background(), "/p/README.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	names := []string{got[0].Metadata["name"], got[1].Metadata["name"], got[2].Metadata["name"]}
	want := []string{"README.md", "README.md#Install", "README.md#Flags"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names %v", names)
		}
	}
	if got[1].Type != "section" || got[1].Metadata["start_line"] != "3" || got[1].Metadata["end_line"] != "5" {
		t.Fatalf("%+v", got[1])
	}
}

func TestTextParserBlocksForOtherFiles(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 70; i++ {
		b.WriteString("key: value\n")
		if i%10 == 9 {
			b.WriteString("\n")
		}
	}
	got, err := TextParser{}.Parse(context.Background(), "/p/conf.yaml", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0].Type != "block" || got[0].Metadata["name"] != "conf.yaml:1" {
		t.Fatalf("%+v", got)
	}
}

func TestTextParserEmptyFile(t *testing.T) {
	got, err := TextParser{}.Parse(context.Background(), "/p/a.md", []byte("  \n\n"))
	if err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestTextParserHeadingInsideFenceIsNotASection(t *testing.T) {
	src := "# Real\n```sh\n# comment\n```\n"
	got, _ := TextParser{}.Parse(context.Background(), "/p/a.md", []byte(src))
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/parser/ -run TestTextParser -count=1`

Expected: FAIL, `TextParser` undefined.

- [ ] **Step 3: Implement**

```go
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
		heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
		if heading == "" || !strings.HasPrefix(line, strings.Repeat("#", len(line)-len(strings.TrimLeft(line, "#")))+" ") {
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
```

Note on `markdownSections`: a heading is `#` to `######` followed by a space. Lines such as `#hashtag` stay in the section. The `HasPrefix(... Repeat("#", n) + " ")` test above encodes that.

- [ ] **Step 4: Register the type**

`router.go` `openParser`, add before `default`:

```go
case "text":
	return TextParser{}, nil
```

`config.go` parser type switch, add:

```go
case "text":
```

`doctor.go` `checkPlugins`, add a `case "text":` that appends `"text"` to `parts` and nothing to `problems`. Read the end of `checkPlugins` first: `parts` feeds a parser fingerprint, so a constant `text` entry is correct.

Check `config/modifier.go`: it only touches `wasm` entries, so `text` entries are left alone. Confirm with `grep -n '"text"' internal/config/modifier.go` returning nothing and `TestModifier*` passing.

- [ ] **Step 5: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/parser/ ./internal/config/ ./cmd/ -count=1 -timeout 180s -skip TestE2EGoParser`

Expected: PASS.

- [ ] **Step 6: Document**

`docs/parsers.md`: a `text` section with the YAML entry, the two chunking rules, and the naming scheme (`README.md#Install`, `conf.yaml:1`). State that `include` globs must also cover those files.

- [ ] **Step 7: Commit**

```bash
git add internal/parser/text.go internal/parser/text_test.go internal/parser/router.go internal/config/config.go cmd/doctor.go docs/parsers.md
git commit -m "Add a built-in text parser for files without a language parser."
```

---

### Task 7: Optional reranker

**Files:**

- Create: `internal/embedder/rerank.go`
- Test: `internal/embedder/rerank_test.go`
- Modify: `internal/config/config.go` (`Config`, `mergeConfig`, `expand`, validation)
- Modify: `internal/query/query.go`, `internal/query/query_test.go`
- Modify: `internal/mcp/server.go` (`NewServer`), `cmd/search.go` and `internal/query/query.go` `Open`
- Modify: `docs/configuration.md`

**Interfaces:**

- Produces: `type Reranker interface{ Rerank(ctx, query string, docs []string, topN int) ([]int, error) }` (indices into `docs`, best first); `func NewReranker(cfg config.Reranker) (Reranker, error)` returns `(nil, nil)` when `cfg.Provider == ""`; config block:

```yaml
reranker:
  provider: voyage          # empty disables it
  model: rerank-2
  api_key: ${VOYAGE_API_KEY}
  candidates: 30            # fused hits sent to the reranker
```

- Consumes: `postJSON`, `newHTTP`, `joinURL` from `internal/embedder/embedder.go`; `Searcher`

- [ ] **Step 1: Write the failing tests**

`internal/embedder/rerank_test.go`:

```go
package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/axitdev/onfly/internal/config"
)

func TestVoyageRerankOrdersByScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"index": 2, "relevance_score": 0.9},
			{"index": 0, "relevance_score": 0.4},
		}})
	}))
	defer srv.Close()
	rr, err := NewReranker(config.Reranker{Provider: "voyage", Endpoint: srv.URL, Model: "rerank-2", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := rr.Rerank(context.Background(), "q", []string{"a", "b", "c"}, 2)
	if err != nil || len(got) != 2 || got[0] != 2 || got[1] != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestNewRerankerDisabled(t *testing.T) {
	rr, err := NewReranker(config.Reranker{})
	if rr != nil || err != nil {
		t.Fatalf("%v %v", rr, err)
	}
}
```

Append to `internal/query/query_test.go`:

```go
type stubRerank struct {
	order []int
	err   error
}

func (r stubRerank) Rerank(context.Context, string, []string, int) ([]int, error) {
	return r.order, r.err
}

func TestRerankReordersFusedHits(t *testing.T) {
	s := newSearcher(t)
	s.Rerank = stubRerank{order: []int{1, 0}}
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "b.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRerankErrorKeepsFusedOrder(t *testing.T) {
	s := newSearcher(t)
	s.Rerank = stubRerank{err: context.DeadlineExceeded}
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "a.go" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRerankIgnoresOutOfRangeAndDuplicateIndices(t *testing.T) {
	s := newSearcher(t)
	s.Rerank = stubRerank{order: []int{7, 1, 1, -1}}
	got, err := s.Search(context.Background(), "getUserId", 5, "")
	if err != nil || len(got) != 2 || got[0].FilePath != "b.go" || got[1].FilePath != "a.go" {
		t.Fatalf("%+v %v", got, err)
	}
}
```

The last test pins that unranked hits keep their fused order after the reranked ones.

- [ ] **Step 2: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/embedder/ ./internal/query/ -run 'Rerank' -count=1`

Expected: FAIL to compile.

- [ ] **Step 3: Add the config block**

In `config.go`:

```go
type Reranker struct {
	Provider   string `yaml:"provider"`
	Endpoint   string `yaml:"endpoint"`
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	Candidates int    `yaml:"candidates"`
}
```

Add `Reranker Reranker \`yaml:"reranker"\`` to `Config`. In `mergeConfig`, copy each non-empty child field the way the embedder block does. In `expand`, run `expandOne` over `Reranker.Endpoint`, `Reranker.APIKey` and `Reranker.Model`. In validation, add:

```go
switch c.Reranker.Provider {
case "", "voyage":
default:
	return fmt.Errorf("reranker.provider %q is not available", c.Reranker.Provider)
}
```

- [ ] **Step 4: Implement the Voyage reranker**

`internal/embedder/rerank.go`:

```go
package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/axitdev/onfly/internal/config"
)

type Reranker interface {
	// Rerank returns indices into docs, most relevant first, at most topN.
	Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error)
}

func NewReranker(cfg config.Reranker) (Reranker, error) {
	switch cfg.Provider {
	case "":
		return nil, nil
	case "voyage":
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = "https://api.voyageai.com"
		}
		if cfg.Model == "" {
			return nil, fmt.Errorf("reranker.model is required")
		}
		key := cfg.APIKey
		if key == "" {
			key = os.Getenv("VOYAGE_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("reranker: voyage api key is required")
		}
		return &voyageReranker{url: joinURL(endpoint, "/v1/rerank"), model: cfg.Model, key: key, client: newHTTP()}, nil
	default:
		return nil, fmt.Errorf("reranker provider %q is not available", cfg.Provider)
	}
}

type voyageReranker struct {
	url, model, key string
	client          *http.Client
}

func (r *voyageReranker) Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error) {
	body, err := json.Marshal(map[string]any{
		"model": r.model, "query": query, "documents": docs, "top_k": topN, "truncation": true,
	})
	if err != nil {
		return nil, err
	}
	raw, err := postJSON(ctx, r.client, r.url, r.key, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			Index int `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("reranker: %w", err)
	}
	out := make([]int, 0, len(resp.Data))
	for _, d := range resp.Data {
		out = append(out, d.Index)
	}
	return out, nil
}
```

Voyage returns `data` sorted by score descending. If a future check shows otherwise, sort by `relevance_score` here, not in the caller.

- [ ] **Step 5: Hook it into the Searcher**

In `query.go` add fields `Rerank embedder.Reranker` and `Candidates int` to `Searcher`, and call `fused = s.rerank(ctx, q, fused)` between `Fuse` and the limit truncation:

```go
const defaultRerankPool = 30

func (s *Searcher) rerank(ctx context.Context, q string, fused []parser.Chunk) []parser.Chunk {
	if s.Rerank == nil || len(fused) < 2 {
		return fused
	}
	pool := s.Candidates
	if pool <= 0 {
		pool = defaultRerankPool
	}
	head := fused
	if len(head) > pool {
		head = fused[:pool]
	}
	docs := make([]string, len(head))
	for i, c := range head {
		docs[i] = c.Content
	}
	order, err := s.Rerank.Rerank(ctx, q, docs, len(head))
	if err != nil {
		s.logger().Warn("rerank", "err", err)
		return fused
	}
	used := make([]bool, len(head))
	out := make([]parser.Chunk, 0, len(fused))
	for _, i := range order {
		if i < 0 || i >= len(head) || used[i] {
			continue
		}
		used[i] = true
		out = append(out, head[i])
	}
	for i, c := range head {
		if !used[i] {
			out = append(out, c)
		}
	}
	return append(out, fused[len(head):]...)
}
```

- [ ] **Step 6: Wire construction**

In `query.Open`, after `embedder.New`: `rr, err := embedder.NewReranker(cfg.Reranker)` (close the store and return on error), then set `Rerank: rr, Candidates: cfg.Reranker.Candidates` on the Searcher. In `mcp.NewServer`, build the reranker the same way and set the same two fields on `h.searcher`; return the error to the caller. `NewServer`'s signature does not change.

- [ ] **Step 7: Run and commit**

Run: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./internal/... ./cmd/ -count=1 -timeout 300s -skip TestE2EGoParser`

Expected: PASS.

`docs/configuration.md`: add the `reranker` block, say it is off by default, that a rerank failure falls back to fused order, and that it adds one HTTP call per search.

```bash
git add internal/embedder/rerank.go internal/embedder/rerank_test.go internal/config/config.go internal/query/query.go internal/query/query_test.go internal/mcp/server.go docs/configuration.md
git commit -m "Add an optional reranker after fusion."
```

---

### Task 8: `get_callees` and `get_impact`

**Files:**

- Create: `internal/mcp/graph.go`
- Test: `internal/mcp/graph_test.go`
- Modify: `internal/mcp/server.go` (tool registration near line 168)
- Modify: `docs/mcp.md`

**Interfaces:**

- Consumes: `Store.GetComponentAST`, `Store.GetReferences`, `toHits`, `fit`-style budget helpers (`h.fitBudget`, `fitHits`), `contentWithRules`, `h.record`
- Produces: MCP tools `get_callees` (input `nameIn`) and `get_impact` (input `impactIn{Name string; Depth int}`), both returning `hitList`

Definitions:

- `get_callees(name)`: the chunks for each name in `GetComponentAST(name).Outbound`, skipping names with no component. Capped at 50.
- `get_impact(name, depth)`: breadth-first walk over `GetReferences`. Depth defaults to 2 and is clamped to 1 to 4. The result is every distinct caller chunk, nearest first, capped at 100. A visited set on chunk key stops cycles.

- [ ] **Step 1: Write the failing tests**

Follow the setup in `server_test.go` `TestTools` (it builds a server over a real SQLite store with `fakeEmbedder`). Read lines 39-120 first and reuse its helper for opening a session and its chunk fixtures.

```go
func TestImpactWalksCallersAndStopsOnCycles(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t) // the helper TestTools uses to build an initialized SQLite store
	vec := []float32{1, 0, 0, 1}
	chunks := []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: "func A() { B() }", Metadata: map[string]string{"name": "A"}, Outbound: []string{"B"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "b.go", Type: "function", Content: "func B() { A(); C() }", Metadata: map[string]string{"name": "B"}, Outbound: []string{"A", "C"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "c.go", Type: "function", Content: "func C() {}", Metadata: map[string]string{"name": "C"}}, Vector: vec},
	}
	if err := storetest.Save(ctx, st, chunks); err != nil {
		t.Fatal(err)
	}
	h := &tools{store: st}
	got, err := h.impact(ctx, "C", 4)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, c := range got {
		names = append(names, c.Metadata["name"])
	}
	// C is called by B, B is called by A, A is called by B again (cycle).
	if len(got) != 2 || names[0] != "B" || names[1] != "A" {
		t.Fatalf("%v", names)
	}
}

func TestCalleesReturnsDefinedNamesOnly(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	if err := storetest.Save(ctx, st, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: "func A() { B(); fmt.Println() }", Metadata: map[string]string{"name": "A"}, Outbound: []string{"B", "Println"}}, Vector: vec},
		{Chunk: parser.Chunk{FilePath: "b.go", Type: "function", Content: "func B() {}", Metadata: map[string]string{"name": "B"}}, Vector: vec},
	}); err != nil {
		t.Fatal(err)
	}
	h := &tools{store: st}
	got, err := h.callees(ctx, "A")
	if err != nil || len(got) != 1 || got[0].Metadata["name"] != "B" {
		t.Fatalf("%+v %v", got, err)
	}
}
```

If `TestTools` has no `openTestStore`-style helper, add one in `graph_test.go` that mirrors its first lines (OpenSQLite in `t.TempDir()`, `Init(ctx, 4)`, `t.Cleanup(Close)`).

- [ ] **Step 2: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/mcp/ -run 'TestImpact|TestCallees' -count=1`

Expected: FAIL, `impact` and `callees` undefined.

- [ ] **Step 3: Implement the walk**

```go
package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxCallees   = 50
	maxImpact    = 100
	defaultDepth = 2
	maxDepth     = 4
)

type impactIn struct {
	Name  string `json:"name" jsonschema:"component name from chunk metadata"`
	Depth int    `json:"depth,omitempty" jsonschema:"caller levels to follow, from 1 to 4; default 2"`
}

func (h *tools) callees(ctx context.Context, name string) ([]parser.Chunk, error) {
	root, err := h.store.GetComponentAST(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []parser.Chunk
	for _, ref := range root.Outbound {
		if len(out) >= maxCallees {
			break
		}
		c, err := h.store.GetComponentAST(ctx, ref)
		if errors.Is(err, vectorstore.ErrNotFound) {
			continue // a library or builtin name, not defined in this index
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func (h *tools) impact(ctx context.Context, name string, depth int) ([]parser.Chunk, error) {
	if depth < 1 {
		depth = defaultDepth
	}
	depth = min(depth, maxDepth)
	seenName := map[string]bool{name: true}
	seenChunk := map[string]bool{}
	frontier := []string{name}
	var out []parser.Chunk
	for level := 0; level < depth && len(frontier) > 0 && len(out) < maxImpact; level++ {
		var next []string
		for _, n := range frontier {
			refs, err := h.store.GetReferences(ctx, n)
			if err != nil {
				return nil, err
			}
			for _, c := range refs {
				key := query.ChunkKey(c)
				if seenChunk[key] {
					continue
				}
				seenChunk[key] = true
				out = append(out, c)
				if len(out) >= maxImpact {
					return out, nil
				}
				if cn := c.Metadata["name"]; cn != "" && !seenName[cn] {
					seenName[cn] = true
					next = append(next, cn)
				}
			}
		}
		frontier = next
	}
	return out, nil
}

func (h *tools) getCallees(ctx context.Context, _ *sdkmcp.CallToolRequest, in nameIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	chunks, err := h.callees(ctx, in.Name)
	if errors.Is(err, vectorstore.ErrNotFound) {
		err = fmt.Errorf("component %q not found", in.Name)
	}
	return h.finishGraph(ctx, "get_callees", in, start, chunks, err)
}

func (h *tools) getImpact(ctx context.Context, _ *sdkmcp.CallToolRequest, in impactIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	chunks, err := h.impact(ctx, in.Name, in.Depth)
	return h.finishGraph(ctx, "get_impact", in, start, chunks, err)
}

func (h *tools) finishGraph(_ context.Context, tool string, in any, start time.Time, chunks []parser.Chunk, err error) (*sdkmcp.CallToolResult, hitList, error) {
	if err != nil {
		h.record(tool, in, start, nil, 0)
		return nil, hitList{}, err
	}
	budget, text := h.fitBudget(chunkQuery(chunks))
	hits, err := fitHits(chunks, budget)
	h.record(tool, in, start, hits, len(hits))
	out := hitList{Rules: text, Hits: hits}
	if out.Hits == nil {
		out.Hits = []hitDTO{}
	}
	return contentWithRules(text, out), out, err
}
```

Export `chunkKey` from `internal/query` as `ChunkKey` (rename it and its two uses in `query.go`) so the walk shares the key with fusion.

Register next to `get_references`:

```go
sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_callees", Description: "Return the chunks a symbol calls, for names defined in this index."}, h.getCallees)
sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_impact", Description: "Return the chunks that call a symbol, directly and through callers, nearest first."}, h.getImpact)
```

- [ ] **Step 4: Run, document, commit**

Run: `CGO_ENABLED=0 go test ./internal/mcp/ ./internal/query/ -count=1 -timeout 180s`

Expected: PASS.

`docs/mcp.md`: add both tools to the tool list and to the token-budget sentence that names `get_references`. State that `get_impact` follows names, so two functions with the same name share callers.

```bash
git add internal/mcp/graph.go internal/mcp/graph_test.go internal/mcp/server.go internal/query/query.go docs/mcp.md
git commit -m "Add get_callees and get_impact tools."
```

---

### Task 9: Overload disambiguation for `get_component_ast`

**Files:**

- Modify: `internal/vectorstore/store.go`, `internal/vectorstore/sqlite.go`, `internal/vectorstore/postgres.go`
- Modify: `internal/vectorstore/contract_test.go`
- Modify: `internal/mcp/server.go` (`nameIn`, `chunkDTO`, `component`)
- Test: `internal/mcp/server_test.go`
- Modify: `docs/mcp.md`

**Interfaces:**

- Produces: `GetComponents(ctx context.Context, name string) ([]parser.Chunk, error)` on `Store`, `*SQLite`, `*Postgres`. It returns one assembled chunk per `(file_path, metadata.receiver)` group, in first-seen row order. `ErrNotFound` when none. `GetComponentAST` is unchanged.
- `nameIn` gets `Receiver string \`json:"receiver,omitempty"\``. `chunkDTO` gets `Alternatives []altDTO \`json:"alternatives,omitempty"\``, `type altDTO struct{ FilePath, Receiver string; StartLine int }`. The `get_component_ast` tool keeps using `nameIn`, so `get_references` sees an unused field it ignores.

- [ ] **Step 1: Write the failing tests**

Contract (inside `runStoreContract`, after the existing component assertions; read the function to find `st`, `ctx` and the fixture vector variable names):

```go
saveA := parser.Chunk{FilePath: "u.go", Type: "method", Content: "func (u User) Save() {}", Metadata: map[string]string{"name": "Save", "receiver": "User"}}
saveB := parser.Chunk{FilePath: "o.go", Type: "method", Content: "func (o Order) Save() {}", Metadata: map[string]string{"name": "Save", "receiver": "Order"}}
if err := st.CommitFile(ctx, "u.go", "h-u", []EmbeddedChunk{{Chunk: saveA, Vector: near}}); err != nil {
	t.Fatal(err)
}
if err := st.CommitFile(ctx, "o.go", "h-o", []EmbeddedChunk{{Chunk: saveB, Vector: near}}); err != nil {
	t.Fatal(err)
}
comps, err := st.GetComponents(ctx, "Save")
if err != nil || len(comps) != 2 || comps[0].Metadata["receiver"] != "User" || comps[1].Metadata["receiver"] != "Order" {
	t.Fatalf("components = %+v %v", comps, err)
}
if _, err := st.GetComponents(ctx, "NoSuchName"); !errors.Is(err, ErrNotFound) {
	t.Fatalf("missing name: %v", err)
}
```

MCP test in `server_test.go`, using the same session helper as `TestTools`: index the two `Save` chunks above, call `get_component_ast` with `{"name":"Save"}` and assert the structured result has one chunk plus an `alternatives` entry whose `receiver` is the other one; call it with `{"name":"Save","receiver":"Order"}` and assert the returned chunk's `metadata.receiver` is `Order` and `alternatives` is empty.

- [ ] **Step 2: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/vectorstore/ ./internal/mcp/ -count=1 -timeout 180s`

Expected: FAIL to compile, `GetComponents` missing.

- [ ] **Step 3: Implement the store method**

`util.go` (next to `assembleComponent`):

```go
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
```

SQLite and Postgres: copy `GetComponentAST`'s query and call `assembleComponents(chunks)`. Add the method to the `Store` interface after `GetComponentAST`. Search `internal/` and `cmd/` for other `Store` implementations (fakes) with `grep -rn "GetComponentAST(ctx" --include='*.go'`; add the method to each.

- [ ] **Step 4: Implement the tool change**

In `component`:

```go
comps, err := h.store.GetComponents(ctx, in.Name)
if errors.Is(err, vectorstore.ErrNotFound) {
	err = fmt.Errorf("component %q not found", in.Name)
}
if err != nil {
	h.record("get_component_ast", in, start, nil, 0)
	return nil, chunkDTO{}, err
}
pick := 0
if in.Receiver != "" {
	pick = -1
	for i, c := range comps {
		if c.Metadata["receiver"] == in.Receiver {
			pick = i
			break
		}
	}
	if pick < 0 {
		h.record("get_component_ast", in, start, nil, 0)
		return nil, chunkDTO{}, fmt.Errorf("component %q has no receiver %q", in.Name, in.Receiver)
	}
}
chunk := comps[pick]
dto := toDTO([]parser.Chunk{chunk})[0]
for i, c := range comps {
	if i == pick {
		continue
	}
	dto.Alternatives = append(dto.Alternatives, altDTO{
		FilePath: c.FilePath, Receiver: c.Metadata["receiver"], StartLine: lineNum(c.Metadata["start_line"]),
	})
}
```

Keep the existing `text := h.rulesText(...)`, `h.record(...)` and return lines, using `chunk` where `*chunk` was used.

- [ ] **Step 5: Run, document, commit**

Run: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./internal/... -count=1 -timeout 300s`

Expected: PASS.

`docs/mcp.md`: `get_component_ast` takes an optional `receiver`. Without it, the earliest chunk is returned and other definitions of the name are listed under `alternatives`.

```bash
git add internal/vectorstore/store.go internal/vectorstore/util.go internal/vectorstore/sqlite.go internal/vectorstore/postgres.go internal/vectorstore/contract_test.go internal/mcp/server.go internal/mcp/server_test.go docs/mcp.md
git commit -m "Report same-name components and accept a receiver."
```

---

### Task 10: Reuse stored vectors for unchanged chunks

**Files:**

- Modify: `internal/vectorstore/util.go` (`parseVecLiteral`), `internal/vectorstore/store.go`, `internal/vectorstore/sqlite.go`, `internal/vectorstore/postgres.go`
- Test: `internal/vectorstore/util_test.go` (create if absent), `internal/vectorstore/contract_test.go`
- Modify: `internal/index/index.go` (`indexFile`, `FileResult`, `Stats`), `cmd/index.go`
- Test: `internal/index/index_test.go`
- Modify: `docs/indexing.md`

**Interfaces:**

- Produces: `VectorsByFile(ctx context.Context, path string) (map[string][]float32, error)` on `Store`: content text to its stored vector for that file; `func parseVecLiteral(s string) ([]float32, error)`; `Stats.Reused`, `FileResult.Reused`
- Consumes: `embedChunks`

Why per file: after an edit, a file's old rows are still in the store until `CommitFile` replaces them, so most chunks of an edited file have a vector that is still correct. This avoids re-embedding the unchanged functions of a file that changed.

- [ ] **Step 1: Write the failing tests**

`util_test.go`:

```go
func TestParseVecLiteralRoundTrip(t *testing.T) {
	in := []float32{1, -0.5, 3.25, 0}
	got, err := parseVecLiteral(vecLiteral(in))
	if err != nil || len(got) != len(in) {
		t.Fatalf("%v %v", got, err)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("%v != %v", got, in)
		}
	}
	if _, err := parseVecLiteral("nope"); err == nil {
		t.Fatal("bad literal must fail")
	}
}
```

Contract, inside `runStoreContract` after a file with known content is committed:

```go
vecs, err := st.VectorsByFile(ctx, "far.go")
if err != nil || len(vecs["func getUserId() {}"]) != 4 {
	t.Fatalf("vectors = %v %v", vecs, err)
}
if got, _ := st.VectorsByFile(ctx, "absent.go"); len(got) != 0 {
	t.Fatalf("absent file: %v", got)
}
```

`index_test.go`, reusing that file's fake embedder pattern (read how the existing tests count `EmbedBatch` calls; if none does, add a `countingEmbedder` that records the number of texts):

```go
func TestReindexEmbedsOnlyChangedChunks(t *testing.T) {
	// Index a file with two functions, edit one, reindex.
	// First run embeds 2 chunks; second run embeds 1; stats.Reused == 1.
}
```

Write it with the repo's helper for a temp project and a stub parser. Read the first 120 lines of `index_test.go` to copy the project, parser and config scaffolding; the assertions are: embedder text count after run 1 is 2, after run 2 is 3 (2 + 1), `stats.Reused == 1` on run 2, and `SearchKeyword` for the unchanged function's body still returns it.

- [ ] **Step 2: Run the tests**

Run: `CGO_ENABLED=0 go test ./internal/vectorstore/ ./internal/index/ -count=1 -timeout 180s`

Expected: FAIL to compile.

- [ ] **Step 3: Implement the vector reads**

`util.go`:

```go
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
```

Add `fmt` to the imports of `util.go` if missing.

SQLite (read as JSON text so one parser serves both backends; sqlite-vec provides `vec_to_json`):

```go
func (s *SQLite) VectorsByFile(ctx context.Context, path string) (map[string][]float32, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.content, vec_to_json(v.embedding)
		FROM chunk_meta AS m JOIN chunk_vecs AS v ON v.rowid = m.rowid
		WHERE m.file_path = ?`, slashPath(path))
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
```

If `vec_to_json` is not available in this sqlite-vec build (the contract test fails with "no such function"), select `v.embedding` as a BLOB instead and decode with `binary.LittleEndian.Uint32` and `math.Float32frombits`, four bytes per component. Keep the same function signature.

Postgres:

```go
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
```

Add `VectorsByFile` to the `Store` interface. Add it to every other `Store` implementation the compiler reports.

- [ ] **Step 4: Reuse in `indexFile`**

Replace the embedding block in `indexFile` (the `if len(chunks) > 0 { ... embedChunks ... }` part) with:

```go
var embedded []vectorstore.EmbeddedChunk
reused := 0
if len(chunks) > 0 {
	s.report("embedding", rel)
	prev, perr := s.store.VectorsByFile(ctx, rel)
	if perr != nil {
		s.logger.Warn("vectors by file", "path", rel, "err", perr)
		prev = nil
	}
	embedded, reused, err = s.embedWithReuse(ctx, chunks, prev)
	if err != nil {
		return FileResult{}, fmt.Errorf("embed %s: %w", rel, err)
	}
	for i := range embedded {
		embedded[i].Workspace = s.cfg.Workspace
	}
}
if err := s.store.CommitFile(ctx, rel, hash, embedded); err != nil {
	return FileResult{}, fmt.Errorf("save %s: %w", rel, err)
}
return FileResult{Chunks: len(chunks), Reused: reused}, nil
```

and add:

```go
// embedWithReuse fills in a stored vector for every chunk whose content the
// file already had, and embeds the rest. The result keeps chunk order.
func (s *Session) embedWithReuse(ctx context.Context, chunks []parser.Chunk, prev map[string][]float32) ([]vectorstore.EmbeddedChunk, int, error) {
	dims := s.cfg.Embedder.Dimensions
	out := make([]vectorstore.EmbeddedChunk, len(chunks))
	var todo []int
	reused := 0
	for i, ch := range chunks {
		if v, ok := prev[ch.Content]; ok && len(v) == dims {
			out[i] = vectorstore.EmbeddedChunk{Chunk: ch, Vector: v}
			reused++
			continue
		}
		todo = append(todo, i)
	}
	if len(todo) == 0 {
		return out, reused, nil
	}
	fresh := make([]parser.Chunk, len(todo))
	for j, i := range todo {
		fresh[j] = chunks[i]
	}
	got, err := embedChunks(ctx, s.emb, fresh, batchSize(s.opt))
	if err != nil {
		return nil, 0, err
	}
	for j, i := range todo {
		out[i] = got[j]
	}
	return out, reused, nil
}
```

Add `Reused int` to `FileResult` and `Stats`; in `Run`, add `stats.Reused += result.Reused` next to `stats.Chunks += result.Chunks`. In `cmd/index.go`, extend the summary line with `, reused %d` before `, skipped`, passing `stats.Reused`. Search `cmd/` tests for the old summary string with `grep -rn "indexed %d files\|indexed .* files" cmd/` and update any assertion.

Known limit, stated in `docs/indexing.md`: a vector is reused when the chunk text is identical and the dimension matches. Switching to a different model with the same dimension keeps old vectors for unchanged text. Run `onfly index` against a new database after changing the model, as before.

- [ ] **Step 5: Run and commit**

Run: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./internal/... ./cmd/ -count=1 -timeout 300s -skip TestE2EGoParser`

Expected: PASS.

```bash
git add internal/vectorstore/util.go internal/vectorstore/util_test.go internal/vectorstore/store.go internal/vectorstore/sqlite.go internal/vectorstore/postgres.go internal/vectorstore/contract_test.go internal/index/index.go internal/index/index_test.go cmd/index.go docs/indexing.md
git commit -m "Reuse stored vectors for unchanged chunks on reindex."
```

---

### Task 11: `serve --watch`

**Files:**

- Modify: `internal/mcp/server.go` (`Serve`)
- Modify: `cmd/serve.go`
- Test: `internal/mcp/serve_watch_test.go`
- Modify: `internal/config/config.go` (`Server.Watch bool`), `docs/mcp.md`, `docs/indexing.md`

**Interfaces:**

- Consumes: `watch.Run(ctx, cfg, logger) error`
- Produces: flag `onfly serve --watch` and config `server.watch: true`; `func startWatch(ctx context.Context, cfg config.Config, logger *slog.Logger, run func(context.Context, config.Config, *slog.Logger) error) (stop func())`

`watch.Run` blocks until its context ends and opens its own store and embedder session. SQLite runs in WAL mode with a 5 s busy timeout, so a second connection in the same process is safe. A watcher error is logged and the server keeps running.

- [ ] **Step 1: Write the failing test**

```go
package mcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/config"
)

func TestStartWatchStopsWithContextAndSurvivesFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var running atomic.Bool
	stop := startWatch(context.Background(), config.Config{}, logger, func(ctx context.Context, _ config.Config, _ *slog.Logger) error {
		running.Store(true)
		<-ctx.Done()
		running.Store(false)
		return ctx.Err()
	})
	deadline := time.Now().Add(2 * time.Second)
	for !running.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !running.Load() {
		t.Fatal("watcher never started")
	}
	stop()
	if running.Load() {
		t.Fatal("stop must wait for the watcher to exit")
	}

	failed := make(chan struct{})
	stop = startWatch(context.Background(), config.Config{}, logger, func(context.Context, config.Config, *slog.Logger) error {
		defer close(failed)
		return errors.New("boom")
	})
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("failing watcher did not run")
	}
	stop() // must not panic or block
}
```

- [ ] **Step 2: Run the test**

Run: `CGO_ENABLED=0 go test ./internal/mcp/ -run TestStartWatch -count=1`

Expected: FAIL, `startWatch` undefined.

- [ ] **Step 3: Implement**

In `server.go`:

```go
// startWatch runs the index watcher beside the MCP server. A watcher error is
// logged and does not end the session. stop cancels it and waits.
func startWatch(ctx context.Context, cfg config.Config, logger *slog.Logger, run func(context.Context, config.Config, *slog.Logger) error) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := run(ctx, cfg, logger); err != nil && ctx.Err() == nil {
			logger.Error("watch stopped", "err", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
```

In `Serve`, after `defer closer()`:

```go
if cfg.Server.Watch {
	stopWatch := startWatch(ctx, cfg, logger, watch.Run)
	defer stopWatch()
}
```

Import `github.com/axitdev/onfly/internal/watch`. Check for an import cycle first: `go list -deps ./internal/watch | grep internal/mcp` must print nothing. If `watch` imports `mcp` (it should not; it imports `index`, `ignore`, `config`), move `startWatch` to `cmd/serve.go` and pass a hook instead.

Add `Watch bool \`yaml:"watch"\`` to `config.Server`. In `mergeConfig`, copy it with `if child.Server.Watch { out.Server.Watch = true }`.

`cmd/serve.go`:

```go
var serveWatch bool
// in RunE, after loading cfg:
if serveWatch {
	cfg.Server.Watch = true
}
// in init():
serveCmd.Flags().BoolVar(&serveWatch, "watch", false, "reindex changed files while serving")
```

- [ ] **Step 4: Run, document, commit**

Run: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./internal/... ./cmd/ -count=1 -timeout 300s -skip TestE2EGoParser`

Expected: PASS.

`docs/mcp.md` and `docs/indexing.md`: describe `--watch` and `server.watch`, say it loads the parsers and the embedder in the serve process, and that watcher errors go to the server log, not stdout.

```bash
git add internal/mcp/server.go internal/mcp/serve_watch_test.go internal/config/config.go cmd/serve.go docs/mcp.md docs/indexing.md
git commit -m "Let serve reindex changed files with --watch."
```

---

### Task 12: Docs sweep and cleanup

**Files:**

- Modify: `README.md`, `docs/mcp.md`, `docs/development.md`
- Delete: `2026-10-13-search-and-parsers.md` (untracked, superseded)

- [ ] **Step 1: Check the docs against the code**

Run: `grep -n "search_semantic" -r README.md docs/ internal/mcp/server.go`

Expected: no stale mention of a tool that is not registered. Fix any that remain.

- [ ] **Step 2: Verify the whole tree**

Run: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./... -count=1 -timeout 300s -skip TestE2EGoParser && CGO_ENABLED=0 go build -o /dev/null .`

Expected: PASS.

- [ ] **Step 3: Remove the superseded plan and commit**

```bash
rm 2026-10-13-search-and-parsers.md
git add README.md docs/mcp.md docs/development.md docs/superpowers/plans/2026-10-14-search-quality-and-dx.md
git commit -m "Document the new commands and tools."
```

---

## Self-review notes

- **Spec coverage:** idea 1 (eval) is Task 5, 2 (rerank) Task 7, 3 (text fallback) Task 6, 4 (graph) Task 8, 5 (overloads) Task 9, 6 (cache) Task 10, 8 (serve --watch) Task 11, 9 (search/stats) Tasks 2 and 3, 10 (mcp-config) Task 4. Idea 7 is already implemented and has no task.
- **Task order:** Task 1 first because Tasks 2, 5, 7 and 8 import `internal/query`. Task 3 before nothing else depends on it. Tasks 6 to 11 are independent of each other after Task 1.
- **Known unknowns the implementer must resolve with a test, not a guess:** whether `vec_to_json` exists in this sqlite-vec build (Task 10, fallback given), whether Voyage returns `data` sorted by score (Task 7, noted), and the exact helper names in `server_test.go` (Tasks 8 and 9 say to read `TestTools` first).
