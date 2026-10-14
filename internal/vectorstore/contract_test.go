package vectorstore

import (
	"context"
	"errors"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func runStoreContract(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	user := parser.Chunk{FilePath: "a.php", Type: "function", Content: "function getUserId() {}", Metadata: map[string]string{"name": "User"}}
	other := parser.Chunk{FilePath: "b.php", Type: "function", Content: "function other() {}", Metadata: map[string]string{"name": "Other"}}
	near := []float32{1, 0, 0, 0}
	far := []float32{0, 1, 0, 0}
	if err := saveChunks(ctx, st, []EmbeddedChunk{{Chunk: user, Vector: near}, {Chunk: other, Vector: far}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.SearchSemantic(ctx, near, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].FilePath != "a.php" {
		t.Fatalf("semantic = %+v", got)
	}
	got, err = st.SearchKeyword(ctx, "user", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Content != user.Content {
		t.Fatalf("keyword = %+v", got)
	}
	user.Outbound = []string{"OrderService.Execute"}
	if err := saveChunks(ctx, st, []EmbeddedChunk{{Chunk: user, Vector: near, Workspace: "backend"}}); err != nil {
		t.Fatal(err)
	}
	refs, err := st.GetReferences(ctx, "OrderService.Execute")
	if err != nil || len(refs) != 1 || refs[0].FilePath != "a.php" {
		t.Fatalf("refs = %+v %v", refs, err)
	}
	if refs[0].Outbound[0] != "OrderService.Execute" {
		t.Fatalf("outbound = %+v", refs[0].Outbound)
	}
	got, err = st.SearchKeyword(ctx, "getUserId", 10, "frontend")
	if err != nil || len(got) != 0 {
		t.Fatalf("workspace filter = %+v %v", got, err)
	}
	got, err = st.SearchKeyword(ctx, "getUserId", 10, "backend")
	if err != nil || len(got) != 1 {
		t.Fatalf("backend = %+v %v", got, err)
	}
	names, err := st.WorkspacesForName(ctx, "User")
	if err != nil || len(names) != 1 || names[0] != "backend" {
		t.Fatalf("workspaces = %v %v", names, err)
	}
	names, err = st.WorkspacesForName(ctx, "missing")
	if err != nil || names == nil || len(names) != 0 {
		t.Fatalf("missing workspaces = %v %v", names, err)
	}
	win := user
	win.FilePath = `src\main.go`
	if err := saveChunks(ctx, st, []EmbeddedChunk{{Chunk: win, Vector: near, Workspace: ""}}); err != nil {
		t.Fatal(err)
	}
	files, err := st.ListFiles(ctx)
	if err != nil || !containsPath(files, "src/main.go") {
		t.Fatalf("files = %v %v", files, err)
	}
	before, err := st.Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitFile(ctx, "src/main.go", "abc", nil); err != nil {
		t.Fatal(err)
	}
	hashes, err := st.FileHashes(ctx)
	if err != nil || hashes["src/main.go"] != "abc" {
		t.Fatalf("hashes = %v %v", hashes, err)
	}
	after, err := st.Generation(ctx)
	if err != nil || after <= before {
		t.Fatalf("generation %d -> %d", before, after)
	}
	if err := st.DeleteFiles(ctx, []string{"src/main.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SearchKeyword(ctx, `say "hello"`, 10, ""); err != nil {
		t.Fatal(err)
	}
	comp, err := st.GetComponentAST(ctx, "User")
	if err != nil || comp.Metadata["name"] != "User" {
		t.Fatalf("component = %+v %v", comp, err)
	}
	if _, err := st.GetComponentAST(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
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
	if err := st.DeleteFiles(ctx, []string{"u.go", "o.go"}); err != nil {
		t.Fatal(err)
	}
	replaced := user
	replaced.Content = "function getUserId() { return 1 }"
	if err := saveChunks(ctx, st, []EmbeddedChunk{
		{Chunk: user, Vector: near},
		{Chunk: parser.Chunk{FilePath: "a.php", Type: "function", Content: "second", Metadata: map[string]string{"name": "Two"}}, Vector: near},
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveChunks(ctx, st, []EmbeddedChunk{{Chunk: replaced, Vector: near}}); err != nil {
		t.Fatal(err)
	}
	got, err = st.SearchKeyword(ctx, "return 1", 10, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("replaced = %+v %v", got, err)
	}
	if _, err := st.GetComponentAST(ctx, "Two"); !errors.Is(err, ErrNotFound) {
		t.Fatal("old chunk survived replace")
	}
	if err := saveChunks(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetComponentAST(ctx, "User"); err != nil {
		t.Fatal(err)
	}
	if err := saveChunks(ctx, st, []EmbeddedChunk{{Chunk: user, Vector: []float32{1}}}); err == nil {
		t.Fatal("expected dimension error")
	}
	if _, err := st.GetComponentAST(ctx, "User"); err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx, 8); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("mismatch = %v", err)
	}
	if _, err := st.GetComponentAST(ctx, "User"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFiles(ctx, []string{"a.php", "b.php"}); err != nil {
		t.Fatal(err)
	}
	files, err = st.ListFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %v", files)
	}
	runWorkspaceKNN(t, st)
}

// runWorkspaceKNN checks that a workspace filter still returns `limit` rows when
// another workspace owns the nearest vectors.
func runWorkspaceKNN(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	var chunks []EmbeddedChunk
	for i := 0; i < 8; i++ {
		chunks = append(chunks, EmbeddedChunk{
			Chunk:     parser.Chunk{FilePath: "knn/foreign" + string(rune('a'+i)) + ".go", Type: "function", Content: "foreign", Metadata: map[string]string{"name": "F"}},
			Vector:    []float32{1, 0.01 * float32(i), 0, 0},
			Workspace: "foreign",
		})
	}
	for i := 0; i < 3; i++ {
		chunks = append(chunks, EmbeddedChunk{
			Chunk:     parser.Chunk{FilePath: "knn/mine" + string(rune('a'+i)) + ".go", Type: "function", Content: "mine", Metadata: map[string]string{"name": "M"}},
			Vector:    []float32{0, 1, 0.01 * float32(i), 0},
			Workspace: "mine",
		})
	}
	if err := saveChunks(ctx, st, chunks); err != nil {
		t.Fatal(err)
	}
	got, err := st.SearchSemantic(ctx, []float32{1, 0, 0, 0}, 3, "mine")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("workspace-filtered KNN returned %d rows, want 3", len(got))
	}
	for _, c := range got {
		if c.Content != "mine" {
			t.Fatalf("foreign row leaked: %+v", c)
		}
	}
}

func containsPath(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
