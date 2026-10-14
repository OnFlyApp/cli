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
