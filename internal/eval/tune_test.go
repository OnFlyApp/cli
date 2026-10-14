package eval

import (
	"testing"

	"github.com/axitdev/onfly/internal/parser"
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
	// setting 1 ranks even cases at 1 and odd ones at 2; setting 0 misses all.
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
