package eval

import (
	"context"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

type stubJudge struct {
	answers []string
	calls   int
}

func (s *stubJudge) Complete(context.Context, string) (string, error) {
	a := s.answers[s.calls%len(s.answers)]
	s.calls++
	return a, nil
}

func TestJudgeCountsValidAlternatives(t *testing.T) {
	want := parser.Chunk{FilePath: "y.go", Content: "y", Metadata: map[string]string{"name": "Y"}}
	alt := parser.Chunk{FilePath: "z.go", Content: "z", Metadata: map[string]string{"name": "Z"}}
	cases := []Case{
		{Query: "first", Expect: []Expect{{File: "y.go", Symbol: "Y"}}},
		{Query: "alt", Expect: []Expect{{File: "y.go", Symbol: "Y"}}},
		{Query: "miss", Expect: []Expect{{File: "y.go", Symbol: "Y"}}},
	}
	res := map[string][]parser.Chunk{
		"first": {want, alt},
		"alt":   {alt, want},
		"miss":  {alt},
	}
	j := &stubJudge{answers: []string{`{"relevant": [1]}`, `{"relevant": []}`}}
	got, err := Judge(context.Background(), j, cases, res, false)
	if err != nil {
		t.Fatal(err)
	}
	// "first" is not sent; "alt" (rank 2) gets answer 1: result 1 is a valid answer;
	// "miss" gets []: still a miss.
	if j.calls != 2 || got.Alternatives != 1 || got.Recall != 2.0/3 || got.StrictRecall != 2.0/3 {
		t.Fatalf("%+v calls=%d", got, j.calls)
	}
	if got.MRR != (1+1+0)/3.0 || got.StrictMRR != (1+0.5)/3.0 {
		t.Fatalf("judged MRR must credit the alternative at rank 1: %+v", got)
	}
}

func TestParseRelevant(t *testing.T) {
	cases := map[string][]int{`{"relevant":[2,1,2,9]}`: {1, 0}, "```json\n{\"relevant\": []}\n```": nil, "results 3 and 1": {2, 0}}
	for in, want := range cases {
		got := parseRelevant(in, 3)
		if len(got) != len(want) {
			t.Errorf("%q: %v want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: %v want %v", in, got, want)
			}
		}
	}
}
