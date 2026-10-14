// Package eval scores search results against hand-written expectations.
package eval

import (
	"context"
	"encoding/json"
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

func matches(c parser.Chunk, want Expect, fileLevel bool) bool {
	if c.FilePath != want.File && !strings.HasSuffix(c.FilePath, "/"+want.File) {
		return false
	}
	return fileLevel || want.Symbol == "" || c.Metadata["name"] == want.Symbol
}

// Split keeps every other case: part "even" the cases at even positions
// (0, 2, ...), "odd" the rest, "" all of them. Tuning fits on one half and is
// checked on the other.
func Split(cases []Case, part string) ([]Case, error) {
	var keep int
	switch part {
	case "":
		return cases, nil
	case "even":
		keep = 0
	case "odd":
		keep = 1
	default:
		return nil, fmt.Errorf("split must be even or odd, got %q", part)
	}
	var out []Case
	for i, c := range cases {
		if i%2 == keep {
			out = append(out, c)
		}
	}
	return out, nil
}

// LoadAll loads and concatenates several case files, in order.
func LoadAll(paths []string) ([]Case, error) {
	var all []Case
	for _, p := range paths {
		cs, err := Load(p)
		if err != nil {
			return nil, err
		}
		all = append(all, cs...)
	}
	return all, nil
}

// Rank is the 1-based position of the first expected hit within the first k
// results, or 0. With fileLevel, only the file must match, not the symbol.
func Rank(c Case, got []parser.Chunk, k int, fileLevel bool) int {
	for i, ch := range got {
		if i >= k {
			break
		}
		for _, want := range c.Expect {
			if matches(ch, want, fileLevel) {
				return i + 1
			}
		}
	}
	return 0
}

// Score summarises ranks (0 is a miss) into recall and MRR.
func Score(ranks []int) (recall, mrr float64) {
	if len(ranks) == 0 {
		return 0, 0
	}
	for _, r := range ranks {
		if r > 0 {
			recall++
			mrr += 1 / float64(r)
		}
	}
	n := float64(len(ranks))
	return recall / n, mrr / n
}

// Run scores symbol-level matches; RunLevel can relax them to the file.
func Run(ctx context.Context, cases []Case, search SearchFunc, k int) (Report, error) {
	return RunLevel(ctx, cases, search, k, false)
}

func RunLevel(ctx context.Context, cases []Case, search SearchFunc, k int, fileLevel bool) (Report, error) {
	if k < 1 {
		return Report{}, fmt.Errorf("k must be at least 1, got %d", k)
	}
	rep := Report{K: k, Total: len(cases)}
	var mrr float64
	for _, c := range cases {
		got, err := search(ctx, c.Query, k)
		if err != nil {
			return rep, fmt.Errorf("query %q: %w", c.Query, err)
		}
		rank := Rank(c, got, k, fileLevel)
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

// Baseline is a saved score that later runs must not fall below.
type Baseline struct {
	K      int     `json:"k"`
	Recall float64 `json:"recall"`
	MRR    float64 `json:"mrr"`
}

// SaveBaseline writes rep's scores to path.
func SaveBaseline(path string, rep Report) error {
	raw, err := json.MarshalIndent(Baseline{K: rep.K, Recall: rep.Recall, MRR: rep.MRR}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// CheckBaseline fails when rep scores below the baseline at path.
func CheckBaseline(path string, rep Report) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read baseline (create it with --update-baseline): %w", err)
	}
	var b Baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return fmt.Errorf("parse baseline %s: %w", path, err)
	}
	if b.K != rep.K {
		return fmt.Errorf("baseline was recorded at k=%d, this run used k=%d", b.K, rep.K)
	}
	const eps = 1e-9
	if rep.Recall < b.Recall-eps {
		return fmt.Errorf("recall %.2f is below baseline %.2f", rep.Recall, b.Recall)
	}
	if rep.MRR < b.MRR-eps {
		return fmt.Errorf("mrr %.3f is below baseline %.3f", rep.MRR, b.MRR)
	}
	return nil
}
