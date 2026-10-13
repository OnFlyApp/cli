package vectorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

func TestSQLiteFileURIDriveLetter(t *testing.T) {
	got := sqliteFileURI(`C:\proj\.onfly\vector.db`)
	want := `file:///C:/proj/.onfly/vector.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)`
	if got != want {
		t.Fatal(got)
	}
	unix := sqliteFileURI(`/tmp/vector.db`)
	if !strings.HasPrefix(unix, `file:///tmp/vector.db?`) {
		t.Fatal(unix)
	}
}

func TestSQLiteDeleteBatches(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	chunks := make([]EmbeddedChunk, 0, 600)
	paths := make([]string, 0, 600)
	for i := 0; i < 600; i++ {
		path := fmt.Sprintf("f%04d.go", i)
		paths = append(paths, path)
		chunks = append(chunks, EmbeddedChunk{
			Chunk:     parser.Chunk{FilePath: path, Type: "function", Content: "x", Metadata: map[string]string{"name": path}},
			Vector:    []float32{1, 0, 0, 0},
			Workspace: "w",
		})
	}
	if err := st.SaveChunks(ctx, chunks); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFiles(ctx, paths); err != nil {
		t.Fatal(err)
	}
	listed, err := st.ListFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("left %d", len(listed))
	}
}

func TestSQLiteVecAndFTS5(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow("select vec_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version == "" {
		t.Fatal("empty vec version")
	}
	if _, err := db.Exec("create virtual table t using fts5(content)"); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteMigratesV1(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vector.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO schema_meta(key, value) VALUES ('dimensions', '4')`,
		`CREATE TABLE chunk_meta (
			rowid INTEGER PRIMARY KEY,
			file_path TEXT NOT NULL,
			type TEXT NOT NULL,
			content TEXT NOT NULL,
			metadata TEXT NOT NULL
		)`,
		`INSERT INTO chunk_meta(file_path, type, content, metadata) VALUES ('a.go', 'function', 'func A', '{"name":"A"}')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetComponentAST(ctx, "A")
	if err != nil || got.Content != "func A" {
		t.Fatalf("%+v %v", got, err)
	}
	hashes, err := st.FileHashes(ctx)
	if err != nil || len(hashes) != 0 {
		t.Fatalf("hashes = %v %v", hashes, err)
	}
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx, 8); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("mismatch = %v", err)
	}
	got, err = st.GetComponentAST(ctx, "A")
	if err != nil || got.Content != "func A" {
		t.Fatalf("after mismatch %+v %v", got, err)
	}
}

func TestSQLiteStore(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "vector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	runStoreContract(t, st)
}

func TestSQLiteOptimizeKeepsRows(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "vector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	ch := parser.Chunk{FilePath: "a.go", Type: "function", Content: "func A() {}", Metadata: map[string]string{"name": "A"}}
	if err := st.SaveChunks(ctx, []EmbeddedChunk{{Chunk: ch, Vector: []float32{1, 0, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Optimize(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetComponentAST(ctx, "A")
	if err != nil || got.Content != ch.Content {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSQLiteRejectsUnknownSchema(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "vector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE schema_meta SET value = '9' WHERE key = 'version'`); err != nil {
		t.Fatal(err)
	}
	err = st.Init(ctx, 4)
	if err == nil || !strings.Contains(err.Error(), "cannot be migrated") {
		t.Fatalf("err = %v", err)
	}
	var version string
	if err := st.db.QueryRow(`SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "9" {
		t.Fatalf("version = %s", version)
	}
}

func TestSQLiteWorkspacesForName(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "vector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	err = st.SaveChunks(ctx, []EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "a.go", Type: "function", Content: "a", Metadata: map[string]string{"name": "User"}}, Vector: []float32{1, 0, 0, 0}, Workspace: "frontend"},
		{Chunk: parser.Chunk{FilePath: "b.go", Type: "function", Content: "b", Metadata: map[string]string{"name": "User"}}, Vector: []float32{0, 1, 0, 0}, Workspace: "backend"},
		{Chunk: parser.Chunk{FilePath: "c.go", Type: "function", Content: "c", Metadata: map[string]string{"name": "User"}}, Vector: []float32{0, 0, 1, 0}, Workspace: ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.WorkspacesForName(ctx, "User")
	if err != nil || len(got) != 2 || got[0] != "backend" || got[1] != "frontend" {
		t.Fatalf("%v %v", got, err)
	}
	got, err = st.WorkspacesForName(ctx, "Missing")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("missing %v %v", got, err)
	}
}

func TestSQLiteSemanticWorkspaceIsNotHidden(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "vector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	var batch []EmbeddedChunk
	for i := 0; i < 1000; i++ {
		batch = append(batch, EmbeddedChunk{
			Chunk:     parser.Chunk{FilePath: "other.go", Type: "function", Content: "other", Metadata: map[string]string{"name": "Other"}},
			Vector:    []float32{1, 0, 0, 0},
			Workspace: "other",
		})
	}
	if err := st.SaveChunks(ctx, batch); err != nil {
		t.Fatal(err)
	}
	mine := EmbeddedChunk{
		Chunk:     parser.Chunk{FilePath: "mine.go", Type: "function", Content: "mine", Metadata: map[string]string{"name": "Mine"}},
		Vector:    []float32{0.9, 0.1, 0, 0},
		Workspace: "mine",
	}
	if err := st.SaveChunks(ctx, []EmbeddedChunk{mine}); err != nil {
		t.Fatal(err)
	}
	got, err := st.SearchSemantic(ctx, []float32{1, 0, 0, 0}, 3, "mine")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FilePath != "mine.go" {
		t.Fatalf("semantic = %+v", got)
	}
}
