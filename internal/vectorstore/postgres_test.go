package vectorstore

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func TestVectorWidthAndHNSWCap(t *testing.T) {
	n, ok := vectorWidth("vector(1024)")
	if !ok || n != 1024 {
		t.Fatalf("%d %v", n, ok)
	}
	if n, ok := vectorWidth("public.vector(3072)"); !ok || n != 3072 {
		t.Fatalf("%d %v", n, ok)
	}
	if _, ok := vectorWidth("text"); ok {
		t.Fatal("text")
	}
	wide := postgresSchema(3072)
	narrow := postgresSchema(1024)
	joined := strings.Join(wide, "\n")
	if strings.Contains(joined, "hnsw") {
		t.Fatal(joined)
	}
	if !strings.Contains(strings.Join(narrow, "\n"), "hnsw") {
		t.Fatal("missing hnsw")
	}
}

func TestPostgresStore(t *testing.T) {
	dsn := os.Getenv("ONFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ONFLY_TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	st, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.pool.Exec(ctx, "DROP TABLE IF EXISTS index_state"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, "DROP TABLE IF EXISTS chunks"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, "DROP TABLE IF EXISTS schema_meta"); err != nil {
		t.Fatal(err)
	}
	runStoreContract(t, st)
	runExtraContract(t, st)
}

func TestPostgresOptimizeKeepsRows(t *testing.T) {
	dsn := os.Getenv("ONFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ONFLY_TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	st, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	ch := parser.Chunk{FilePath: "optimize.go", Type: "function", Content: "func OptimizeProbe() {}", Metadata: map[string]string{"name": "OptimizeProbe"}}
	if err := saveChunks(ctx, st, []EmbeddedChunk{{Chunk: ch, Vector: []float32{1, 0, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteFiles(context.Background(), []string{ch.FilePath}) })
	if err := st.Optimize(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetComponentAST(ctx, "OptimizeProbe")
	if err != nil || got.Content != ch.Content {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPostgresRejectsUnknownSchema(t *testing.T) {
	dsn := os.Getenv("ONFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ONFLY_TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	st, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `UPDATE schema_meta SET value = '2' WHERE key = 'version'`)
	})
	if _, err := st.pool.Exec(ctx, `UPDATE schema_meta SET value = '9' WHERE key = 'version'`); err != nil {
		t.Fatal(err)
	}
	err = st.Init(ctx, 4)
	if err == nil || !strings.Contains(err.Error(), "cannot be migrated") {
		t.Fatalf("err = %v", err)
	}
	var version string
	if err := st.pool.QueryRow(ctx, `SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "9" {
		t.Fatalf("version = %s", version)
	}
}

func TestPostgresStatsAndVectors(t *testing.T) {
	dsn := os.Getenv("ONFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ONFLY_TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	st, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, tbl := range []string{"index_state", "chunks", "schema_meta"} {
		if _, err := st.pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	vec := []float32{0.1, 0, 1e-7, 1}
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
	if n, err := st.UnnamedChunks(ctx); err != nil || n != 1 {
		t.Fatalf("unnamed %d %v", n, err)
	}
	vecs, err := st.VectorsByFile(ctx, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	back := vecs[fn.Content]
	if len(back) != len(vec) {
		t.Fatalf("vectors %v", vecs)
	}
	for i := range vec {
		if d := back[i] - vec[i]; d > 1e-6 || d < -1e-6 {
			t.Fatalf("vec[%d] = %v, want %v", i, back[i], vec[i])
		}
	}
}
