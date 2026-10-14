// Package eval scores search results against hand-written expectations.
package eval

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"go.yaml.in/yaml/v3"
)

type Expect struct {
	File   string `yaml:"file" json:"file"`
	Symbol string `yaml:"symbol,omitempty" json:"symbol,omitempty"`
}

type Case struct {
	Query  string   `yaml:"query" json:"query"`
	Expect []Expect `yaml:"expect" json:"expect"`
}

type CaseResult struct {
	Query string `json:"query"`
	Rank  int    `json:"rank"` // 1-based; 0 is a miss
}

type Report struct {
	K      int          `json:"k"`
	Total  int          `json:"total"`
	Hits   int          `json:"hits"`
	Recall float64      `json:"recall"`
	MRR    float64      `json:"mrr"`
	Cases  []CaseResult `json:"cases"`
}

type SearchFunc func(ctx context.Context, q string, limit int) ([]parser.Chunk, error)

func Load(path string) ([]Case, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Cases []Case `yaml:"cases"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Cases) == 0 {
		return nil, fmt.Errorf("%s: no cases", path)
	}
	for i, c := range doc.Cases {
		if strings.TrimSpace(c.Query) == "" || len(c.Expect) == 0 {
			return nil, fmt.Errorf("%s: case %d needs a query and at least one expectation", path, i+1)
		}
		for _, e := range c.Expect {
			if e.File == "" {
				return nil, fmt.Errorf("%s: case %d has an expectation without a file", path, i+1)
			}
		}
	}
	return doc.Cases, nil
}

func matches(c parser.Chunk, want Expect) bool {
	if !strings.HasSuffix(c.FilePath, want.File) {
		return false
	}
	return want.Symbol == "" || c.Metadata["name"] == want.Symbol
}

func Run(ctx context.Context, cases []Case, search SearchFunc, k int) (Report, error) {
	rep := Report{K: k, Total: len(cases)}
	var mrr float64
	for _, c := range cases {
		got, err := search(ctx, c.Query, k)
		if err != nil {
			return rep, fmt.Errorf("query %q: %w", c.Query, err)
		}
		rank := 0
	scan:
		for i, ch := range got {
			if i >= k {
				break
			}
			for _, want := range c.Expect {
				if matches(ch, want) {
					rank = i + 1
					break scan
				}
			}
		}
		if rank > 0 {
			rep.Hits++
			mrr += 1 / float64(rank)
		}
		rep.Cases = append(rep.Cases, CaseResult{Query: c.Query, Rank: rank})
	}
	if rep.Total > 0 {
		rep.Recall = float64(rep.Hits) / float64(rep.Total)
		rep.MRR = mrr / float64(rep.Total)
	}
	return rep, nil
}
