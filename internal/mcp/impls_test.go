package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

func TestShortNames(t *testing.T) {
	got := shortNames(`Shape, \Ns\Countable`)
	if strings.Join(got, ",") != "Shape,Countable" {
		t.Fatalf("%v", got)
	}
	if got := shortNames("Repo<User, Id>, a.b.Closeable"); strings.Join(got, ",") != "Repo,Closeable" {
		t.Fatalf("%v", got)
	}
	if got := shortNames("std::fmt::Display"); strings.Join(got, ",") != "Display" {
		t.Fatalf("%v", got)
	}
}

func implFixture(t *testing.T) *tools {
	t.Helper()
	v := []float32{1, 0, 0, 1}
	c := func(file, typ, name, content string, meta map[string]string) vectorstore.EmbeddedChunk {
		m := map[string]string{"name": name, "start_line": "1"}
		for k, val := range meta {
			m[k] = val
		}
		return vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: file, Type: typ, Content: content, Metadata: m}, Vector: v}
	}
	method := func(file, recv, name string) vectorstore.EmbeddedChunk {
		return c(file, "method", name, "func (x *"+recv+") "+name+"() error {}", map[string]string{"receiver": recv})
	}
	chunks := []vectorstore.EmbeddedChunk{
		// PHP
		c("app/Shape.php", "interface", "Shape", "interface Shape { public function area(); }", nil),
		c("app/Circle.php", "class", "Circle", "class Circle implements Shape, Countable {}", map[string]string{"implements": "Shape, Countable"}),
		c("app/Square.php", "class", "Square", "class Square extends Base implements \\App\\Shape {}", map[string]string{"extends": "Base", "implements": "\\App\\Shape"}),
		c("app/Other.php", "class", "Other", "class Other implements Reshape {}", map[string]string{"implements": "Reshape"}),
		// Rust
		c("src/shape.rs", "trait", "Shape", "pub trait Shape { fn area(&self) -> f64; }", nil),
		c("src/tri.rs", "impl", "Shape for Tri", "impl Shape for Tri {", map[string]string{"trait": "Shape", "type": "Tri"}),
		// Go
		c("store/store.go", "interface", "Store", "type Store interface {\n\t// Save writes.\n\tSave() error\n\tClose() error\n}", nil),
		c("store/pg.go", "struct", "Postgres", "type Postgres struct{}", nil),
		method("store/pg.go", "Postgres", "Save"), method("store/pg.go", "Postgres", "Close"),
		c("store/lite.go", "struct", "SQLite", "type SQLite struct{}", nil),
		method("store/lite.go", "SQLite", "Save"), method("store/lite.go", "SQLite", "Close"),
		c("other/half.go", "struct", "Half", "type Half struct{}", nil),
		method("other/half.go", "Half", "Close"),
	}
	store := openTestStore(t)
	if err := saveHashed(store, t.TempDir(), chunks); err != nil {
		t.Fatal(err)
	}
	return &tools{store: store, maxTokens: 8192}
}

func names(impls []implementation) string {
	var n []string
	for _, i := range impls {
		n = append(n, i.chunk.Metadata["name"]+":"+i.how)
	}
	return strings.Join(n, " ")
}

func TestImplementationsFromExplicitClauses(t *testing.T) {
	h := implFixture(t)
	ctx := context.Background()
	php, _ := h.store.GetComponents(ctx, "Shape")
	var phpShape, rustShape parser.Chunk
	for _, d := range php {
		if strings.HasSuffix(d.FilePath, ".php") {
			phpShape = d
		} else {
			rustShape = d
		}
	}
	got := names(h.implementationsOf(ctx, phpShape))
	if !strings.Contains(got, "Circle:implements") || !strings.Contains(got, "Square:implements") || strings.Contains(got, "Other") {
		t.Fatalf("a name that merely contains Shape (Reshape) is not an implementation: %s", got)
	}
	if got := names(h.implementationsOf(ctx, rustShape)); !strings.Contains(got, "Shape for Tri:impl") {
		t.Fatalf("%s", got)
	}
}

func TestGoInterfacesAreSatisfiedByMethodSets(t *testing.T) {
	h := implFixture(t)
	ctx := context.Background()
	defs, _ := h.store.GetComponents(ctx, "Store")
	got := names(h.implementationsOf(ctx, defs[0]))
	if !strings.Contains(got, "Postgres:methods") || !strings.Contains(got, "SQLite:methods") || strings.Contains(got, "Half") {
		t.Fatalf("a type needs every method of the interface: %s", got)
	}
	block := h.implementationsBlock(ctx, defs)
	if !strings.Contains(block, `<implementations of="Store">`) || !strings.Contains(block, "likely: has all its methods") {
		t.Fatalf("%s", block)
	}
}

func TestGoInterfaceMethodNames(t *testing.T) {
	got := goInterfaceMethods("type X interface {\n\t// A does.\n\tA(x int) error\n\tB()\n\tio.Reader\n\tA()\n}")
	if strings.Join(got, ",") != "A,B" {
		t.Fatalf("comments, embedded types and repeats are skipped: %v", got)
	}
}
