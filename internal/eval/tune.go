package eval

import (
	"fmt"
	"sort"
)

type Candidate struct {
	Name   string
	Ranks  []int
	Recall float64
	MRR    float64
}

type Tuner struct {
	Rank func(setting int, idx []int) ([]int, error)
	Name func(setting int) string
	N    int
}

type Result struct {
	Best         int
	Fit, Check   Candidate
	Baseline     int
	BaseFit      Candidate
	BaseCheck    Candidate
	Table        []Candidate
	FitIdx, Hold []int
}

func SplitIdx(n int) (fit, check []int) {
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			fit = append(fit, i)
		} else {
			check = append(check, i)
		}
	}
	return fit, check
}

func score(name string, ranks []int) Candidate {
	r, m := Score(ranks)
	return Candidate{Name: name, Ranks: ranks, Recall: r, MRR: m}
}

func Tune(t Tuner, nCases, top int) (Result, error) {
	if t.N < 1 || nCases < 2 {
		return Result{}, fmt.Errorf("tune needs at least one setting and two cases")
	}
	fit, check := SplitIdx(nCases)
	res := Result{FitIdx: fit, Hold: check}
	all := make([]Candidate, t.N)
	for s := 0; s < t.N; s++ {
		ranks, err := t.Rank(s, fit)
		if err != nil {
			return res, err
		}
		all[s] = score(t.Name(s), ranks)
	}
	order := make([]int, t.N)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return all[order[a]].MRR > all[order[b]].MRR })
	res.Best = order[0]
	res.Fit = all[res.Best]
	res.BaseFit = all[0]
	for _, s := range order {
		if len(res.Table) >= top {
			break
		}
		res.Table = append(res.Table, all[s])
	}
	var err error
	var ranks []int
	if ranks, err = t.Rank(res.Best, check); err != nil {
		return res, err
	}
	res.Check = score(t.Name(res.Best), ranks)
	if ranks, err = t.Rank(0, check); err != nil {
		return res, err
	}
	res.BaseCheck = score(t.Name(0), ranks)
	return res, nil
}
