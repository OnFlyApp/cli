package eval

import (
	"github.com/axitdev/onfly/internal/usage"
	"go.yaml.in/yaml/v3"
)

// MaxMinedExpect caps the expectations kept per mined query.
const MaxMinedExpect = 3

// Mine turns usage events into cases: each query expects the chunks an
// assistant opened after it. Queries keep first-seen order; a chunk opened
// twice for one query counts once.
func Mine(events []usage.Event) []Case {
	var order []string
	by := map[string][]Expect{}
	for _, e := range events {
		exp, ok := by[e.Query]
		if !ok {
			order = append(order, e.Query)
		}
		dup := false
		for _, x := range exp {
			if x.File == e.File && x.Symbol == e.Symbol {
				dup = true
			}
		}
		if !dup && len(exp) < MaxMinedExpect {
			by[e.Query] = append(exp, Expect{File: e.File, Symbol: e.Symbol})
		} else if !ok {
			by[e.Query] = exp
		}
	}
	out := make([]Case, 0, len(order))
	for _, q := range order {
		if len(by[q]) > 0 {
			out = append(out, Case{Query: q, Expect: by[q]})
		}
	}
	return out
}

// MarshalCases renders cases as a cases file.
func MarshalCases(cases []Case) ([]byte, error) {
	return yaml.Marshal(struct {
		Cases []Case `yaml:"cases"`
	}{cases})
}
