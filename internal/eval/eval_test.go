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

func TestMatchesIsNotAFuzzySuffix(t *testing.T) {
	if matches(chunk("internal/reindex.go", "X"), Expect{File: "index.go"}) {
		t.Fatal("reindex.go matched index.go")
	}
	if !matches(chunk("internal/index.go", "X"), Expect{File: "index.go"}) || !matches(chunk("index.go", "X"), Expect{File: "index.go"}) {
		t.Fatal("exact and path-suffix must match")
	}
}

func TestRunRejectsNonPositiveK(t *testing.T) {
	_, err := Run(context.Background(), nil, nil, 0)
	if err == nil {
		t.Fatal("want error")
	}
}
