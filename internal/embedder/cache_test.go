package embedder

import (
	"context"
	"testing"
)

type countingEmb struct{ calls int }

func (c *countingEmb) Embed(_ context.Context, text string) ([]float32, error) {
	c.calls++
	return []float32{float32(len(text))}, nil
}
func (c *countingEmb) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return nil, nil
}

func TestCachedEmbedsOnceAndEvictsOldest(t *testing.T) {
	inner := &countingEmb{}
	e := Cached(inner, 2)
	ctx := context.Background()
	for _, q := range []string{"a", "a", "bb", "a"} {
		if _, err := e.Embed(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("calls = %d, want 2", inner.calls)
	}
	e.Embed(ctx, "ccc")
	e.Embed(ctx, "bb")
	if inner.calls != 4 {
		t.Fatalf("calls = %d, want 4", inner.calls)
	}
	v, _ := e.Embed(ctx, "a")
	v[0] = 99
	if w, _ := e.Embed(ctx, "a"); w[0] != 1 {
		t.Fatalf("cache entry was mutated: %v", w)
	}
}
