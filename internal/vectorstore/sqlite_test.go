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
	if err := saveChunks(ctx, st, chunks); err != nil {
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

func TestComponentJoinsParts(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	vec := []float32{1, 0, 0, 0}
	err = saveChunks(ctx, st, []EmbeddedChunk{
		{Chunk: parser.Chunk{
			FilePath: "a.go", Type: "function", Content: "aaaaaaBB",
			Metadata: map[string]string{"name": "User", "part": "1/2", "start_line": "1", "end_line": "2"},
		}, Vector: vec},
		{Chunk: parser.Chunk{
			FilePath: "a.go", Type: "function", Content: "BBcccccc",
			Metadata: map[string]string{"name": "User", "part": "2/2", "start_line": "2", "end_line": "4"},
		}, Vector: vec},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetComponentAST(ctx, "User")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "aaaaaaBBcccccc" || got.Metadata["part"] != "" || got.Metadata["end_line"] != "4" || got.Metadata["start_line"] != "1" {
		t.Fatalf("%q %+v", got.Content, got.Metadata)
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
	if err := saveChunks(ctx, st, []EmbeddedChunk{{Chunk: ch, Vector: []float32{1, 0, 0, 0}}}); err != nil {
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
	err = saveChunks(ctx, st, []EmbeddedChunk{
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
	if err := saveChunks(ctx, st, batch); err != nil {
		t.Fatal(err)
	}
	mine := EmbeddedChunk{
		Chunk:     parser.Chunk{FilePath: "mine.go", Type: "function", Content: "mine", Metadata: map[string]string{"name": "Mine"}},
		Vector:    []float32{0.9, 0.1, 0, 0},
		Workspace: "mine",
	}
	if err := saveChunks(ctx, st, []EmbeddedChunk{mine}); err != nil {
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

func TestSQLiteCosineDistance(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		st, err := OpenSQLite(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		ctx := context.Background()
		if err := st.Init(ctx, 4); err != nil {
			t.Fatal(err)
		}
		assertCosineSchema(t, st)
		if err := saveChunks(ctx, st, []EmbeddedChunk{
			{Chunk: parser.Chunk{FilePath: "scaled.go", Type: "function", Content: "scaled", Metadata: map[string]string{"name": "Scaled"}}, Vector: []float32{3, 0, 0, 0}},
			{Chunk: parser.Chunk{FilePath: "ortho.go", Type: "function", Content: "ortho", Metadata: map[string]string{"name": "Ortho"}}, Vector: []float32{0, 1, 0, 0}},
		}); err != nil {
			t.Fatal(err)
		}
		assertScaledFirst(t, st)
	})
	t.Run("migrated", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "vector.db")
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		stmts := []string{
			`CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
			`INSERT INTO schema_meta(key, value) VALUES ('version', '2'), ('dimensions', '4')`,
			`CREATE TABLE chunk_meta (
				rowid INTEGER PRIMARY KEY,
				file_path TEXT NOT NULL,
				type TEXT NOT NULL,
				content TEXT NOT NULL,
				metadata TEXT NOT NULL,
				outbound TEXT NOT NULL DEFAULT '[]',
				workspace TEXT NOT NULL DEFAULT ''
			)`,
			`INSERT INTO chunk_meta(rowid, file_path, type, content, metadata) VALUES
				(1, 'scaled.go', 'function', 'scaled', '{"name":"Scaled"}'),
				(2, 'ortho.go', 'function', 'ortho', '{"name":"Ortho"}')`,
			`CREATE VIRTUAL TABLE chunk_vecs USING vec0(embedding float[4])`,
			`INSERT INTO chunk_vecs(rowid, embedding) VALUES (1, '[3,0,0,0]'), (2, '[0,1,0,0]')`,
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
		if err := st.Init(context.Background(), 4); err != nil {
			t.Fatal(err)
		}
		assertCosineSchema(t, st)
		assertScaledFirst(t, st)
	})
}

func assertCosineSchema(t *testing.T, st *SQLite) {
	t.Helper()
	var version, create string
	if err := st.db.QueryRow(`SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("version = %s", version)
	}
	if err := st.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'chunk_vecs'`).Scan(&create); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(create, "distance_metric=cosine") {
		t.Fatal(create)
	}
}

func assertScaledFirst(t *testing.T, st *SQLite) {
	t.Helper()
	got, err := st.SearchSemantic(context.Background(), []float32{1, 0, 0, 0}, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].FilePath != "scaled.go" || got[1].FilePath != "ortho.go" {
		t.Fatalf("%+v", got)
	}
}

func TestKeywordSearchIsSubstringAndUpgradesOldFTS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v.db")
	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	// Recreate the FTS table the way an older build did: default tokenizer.
	for _, q := range []string{
		`DROP TABLE chunk_fts`,
		`CREATE VIRTUAL TABLE chunk_fts USING fts5(content, file_path, content='chunk_meta', content_rowid='rowid')`,
	} {
		if _, err := st.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	ch := parser.Chunk{FilePath: "a.go", Type: "function", Content: "func GetUserId() {}", Metadata: map[string]string{"name": "GetUserId"}}
	if err := st.CommitFile(ctx, "a.go", "h", []EmbeddedChunk{{Chunk: ch, Vector: []float32{1, 0, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	for _, kw := range []string{"serid", "GETUSER", "Id", "func GetUserId"} {
		got, err := st.SearchKeyword(ctx, kw, 10, "")
		if err != nil || len(got) != 1 {
			t.Fatalf("%q: %+v %v", kw, got, err)
		}
	}
	if got, err := st.SearchKeyword(ctx, "absent", 10, ""); err != nil || len(got) != 0 {
		t.Fatalf("absent: %+v %v", got, err)
	}
}

func TestUnnamedChunks(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	named := parser.Chunk{FilePath: "a.go", Type: "function", Content: "func A() {}", Metadata: map[string]string{"name": "A"}}
	blank := parser.Chunk{FilePath: "b.go", Type: "file", Content: "package p", Metadata: map[string]string{}}
	vec := []float32{1, 0, 0, 0}
	if err := st.CommitFile(ctx, "a.go", "h1", []EmbeddedChunk{{Chunk: named, Vector: vec}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CommitFile(ctx, "b.go", "h2", []EmbeddedChunk{{Chunk: blank, Vector: vec}}); err != nil {
		t.Fatal(err)
	}
	n, err := st.UnnamedChunks(ctx)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

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
	if e := got.ByExt[".go"]; e.Files != 2 || e.Chunks != 2 || e.Bytes != len(fn.Content)+len(blank.Content) {
		t.Fatalf("ByExt %+v", got.ByExt)
	}
}

func TestExtOf(t *testing.T) {
	for in, want := range map[string]string{
		"a/b.go": ".go", "x/app.min.js": ".min.js", "Makefile": "(none)", "t.blade.php": ".blade.php", ".gitignore": "(none)", "A.JSON": ".json",
	} {
		if got := extOf(in); got != want {
			t.Errorf("extOf(%q) = %q, want %q", in, got, want)
		}
	}
}
