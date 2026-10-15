package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/usage"
)

func TestSplitAndLoadAll(t *testing.T) {
	cases := []Case{{Query: "a"}, {Query: "b"}, {Query: "c"}}
	even, _ := Split(cases, "even")
	odd, _ := Split(cases, "odd")
	if len(even) != 2 || len(odd) != 1 || even[1].Query != "c" || odd[0].Query != "b" {
		t.Fatalf("%v %v", even, odd)
	}
	if all, err := Split(cases, ""); err != nil || len(all) != 3 {
		t.Fatal("empty split keeps everything")
	}
	if _, err := Split(cases, "x"); err == nil {
		t.Fatal("bad split must fail")
	}
	fit, check := SplitIdx(5)
	if len(fit) != 3 || len(check) != 2 || fit[2] != 4 || check[0] != 1 {
		t.Fatalf("%v %v", fit, check)
	}
}

func TestScoreAndRankFileLevel(t *testing.T) {
	r, m := Score([]int{1, 0, 2, 0})
	if r != 0.5 || m != (1+0.5)/4 {
		t.Fatalf("%v %v", r, m)
	}
	c := Case{Expect: []Expect{{File: "a.go", Symbol: "X"}}}
	got := []parser.Chunk{chunk("a.go", "Y")}
	if Rank(c, got, 5, false) != 0 || Rank(c, got, 5, true) != 1 {
		t.Fatal("file level must ignore the symbol")
	}
}

func TestTunePicksBestAndChecksHeldOut(t *testing.T) {
	tu := Tuner{
		N:    2,
		Name: func(s int) string { return []string{"base", "better"}[s] },
		Rank: func(s int, idx []int) ([]int, error) {
			out := make([]int, len(idx))
			for i, ix := range idx {
				switch {
				case s == 0:
					out[i] = 0
				case ix%2 == 0:
					out[i] = 1
				default:
					out[i] = 2
				}
			}
			return out, nil
		},
	}
	res, err := Tune(tu, 6, 5)
	if err != nil || res.Best != 1 || res.Fit.MRR != 1 || res.Check.MRR != 0.5 || res.BaseFit.MRR != 0 || len(res.Table) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := Tune(Tuner{}, 6, 5); err == nil {
		t.Fatal("empty tuner must fail")
	}
}

func TestMineGroupsOpensByQuery(t *testing.T) {
	ev := []usage.Event{
		{Query: "q1", File: "a.go", Symbol: "A"},
		{Query: "q2", File: "b.go"},
		{Query: "q1", File: "a.go", Symbol: "A"},
		{Query: "q1", File: "c.go", Symbol: "C"},
	}
	got := Mine(ev)
	if len(got) != 2 || got[0].Query != "q1" || len(got[0].Expect) != 2 || got[1].Expect[0].File != "b.go" {
		t.Fatalf("%+v", got)
	}
	raw, err := MarshalCases(got)
	if err != nil || !strings.Contains(string(raw), "query: q1") {
		t.Fatalf("%s %v", raw, err)
	}
	path := filepath.Join(t.TempDir(), "m.yaml")
	os.WriteFile(path, raw, 0o644)
	if back, err := Load(path); err != nil || len(back) != 2 {
		t.Fatalf("mined file must load as a cases file: %v %v", back, err)
	}
}

func TestExplainClassifiesWhatTookRankOne(t *testing.T) {
	sec := parser.Chunk{FilePath: "docs/a.md", Type: "section", Content: "doc", Metadata: map[string]string{"name": "A"}}
	other := parser.Chunk{FilePath: "x.go", Type: "function", Content: "x", Metadata: map[string]string{"name": "X"}}
	want := parser.Chunk{FilePath: "y.go", Type: "function", Content: "y", Metadata: map[string]string{"name": "Y"}}
	sib := parser.Chunk{FilePath: "y.go", Type: "function", Content: "z", Metadata: map[string]string{"name": "Z"}}
	cases := []Case{
		{Query: "first", Expect: []Expect{{File: "y.go", Symbol: "Y"}}},
		{Query: "docs", Expect: []Expect{{File: "y.go", Symbol: "Y"}}},
		{Query: "other", Expect: []Expect{{File: "y.go", Symbol: "Y"}}},
		{Query: "sibling", Expect: []Expect{{File: "y.go", Symbol: "Y"}}},
	}
	res := map[string][]parser.Chunk{
		"first":   {want},
		"docs":    {sec, want},
		"other":   {other, want},
		"sibling": {sib, want},
	}
	got := Explain(cases, res, 5, false)
	if len(got) != 3 || got[0].Reason != DocOverAPI || got[1].Reason != OtherCode || got[2].Reason != SameFile || got[0].Rank != 2 {
		t.Fatalf("%+v", got)
	}
	var b strings.Builder
	WriteExplain(&b, got, 4)
	if !strings.Contains(b.String(), "3 of 4 cases were not first") || !strings.Contains(b.String(), "want: y.go#Y") {
		t.Fatal(b.String())
	}
}
