package mcp

import (
	"context"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

func openTestStore(t *testing.T) vectorstore.Store {
	t.Helper()
	st, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestImpactWalksCallersAndStopsOnCycles(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
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
