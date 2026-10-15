package eval

import "testing"

func TestAscendFindsTheMaximum(t *testing.T) {
	f := func(v []float64) float64 {
		d1, d2 := v[0]-3, v[1]-0.5
		return -(d1*d1 + d2*d2)
	}
	cands := func(i int, v float64) []float64 {
		return []float64{v * 0.5, v * 0.8, v * 1.25, v * 2, v + 0.25, v - 0.25}
	}
	a := Ascend([]float64{1, 1}, 30, 1e-9, cands, f)
	if a.Values[0] < 2.5 || a.Values[0] > 3.5 || a.Values[1] < 0.25 || a.Values[1] > 0.75 || a.Score <= f([]float64{1, 1}) {
		t.Fatalf("%+v", a)
	}
	flat := Ascend([]float64{1}, 5, 1e-9, cands, func([]float64) float64 { return 1 })
	if flat.Values[0] != 1 || flat.Evals < 1 {
		t.Fatalf("a flat objective must leave the start alone: %+v", flat)
	}
}
