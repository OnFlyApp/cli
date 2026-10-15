package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/eval"
	"github.com/axitdev/onfly/internal/query"
)

func TestTuneGridStartsWithCurrentSettings(t *testing.T) {
	cur := query.Params{Semantic: 2, Keyword: 0.5, Word: 0.25, Field: 0.5, RRFK: 20}
	grid := tuneGrid(cur)
	if len(grid) < 100 || grid[0].p != cur {
		t.Fatalf("the first setting is the current one: %d settings, first %+v", len(grid), grid[0].p)
	}
	if !strings.Contains(grid[0].label, "semantic=2") || !strings.Contains(grid[0].label, "rrf_k=20") {
		t.Fatalf("%s", grid[0].label)
	}
}

func TestPrintTuneAdoptsOnlyAClearGain(t *testing.T) {
	best := query.Params{Semantic: 1.5, Keyword: 1, Word: 0.1, Field: 1, RRFK: 60}
	r := eval.Result{
		Best:      3,
		BaseCheck: eval.Candidate{MRR: 0.70, Recall: 0.9},
		Check:     eval.Candidate{MRR: 0.75, Recall: 0.92},
		FitIdx:    []int{0, 2}, Hold: []int{1, 3},
	}
	var out bytes.Buffer
	printTune(&out, r, best, 4, 10)
	got := out.String()
	for _, want := range []string{"search:", "semantic_weight: 1.5", "keyword_weight: 1", "rrf_k: 60"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	r.Check.MRR = 0.705
	out.Reset()
	printTune(&out, r, best, 4, 10)
	if !strings.Contains(out.String(), "keep your configuration") || strings.Contains(out.String(), "semantic_weight:") {
		t.Fatalf("a gain under 0.01 must not be adopted:\n%s", out.String())
	}
}

func TestLoadEvalCasesResolvesRelativePaths(t *testing.T) {
	root := t.TempDir()
	body := "cases:\n  - query: where is retry\n    expect:\n      - file: internal/retry.go\n"
	if err := os.WriteFile(filepath.Join(root, "cases.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadEvalCases(root, []string{"cases.yaml"})
	if err != nil || len(got) != 1 || got[0].Query != "where is retry" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := loadEvalCases(root, []string{"missing.yaml"}); err == nil {
		t.Fatal("a missing case file is an error")
	}
}
