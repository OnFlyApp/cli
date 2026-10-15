package eval

// Ascent is the outcome of a coordinate ascent.
type Ascent struct {
	Values []float64
	Score  float64
	Evals  int
}

// Ascend maximises score over a parameter vector by coordinate ascent: each
// round tries, for every parameter, the candidates candidates(i, value)
// returns, keeps the best improvement and moves on. It stops after rounds
// rounds or when a whole round finds no improvement larger than eps. The
// start vector is not modified.
func Ascend(start []float64, rounds int, eps float64, candidates func(i int, v float64) []float64, score func([]float64) float64) Ascent {
	cur := append([]float64(nil), start...)
	best := score(cur)
	a := Ascent{Evals: 1}
	for r := 0; r < rounds; r++ {
		improved := false
		for i := range cur {
			bestV, bestS := cur[i], best
			for _, v := range candidates(i, cur[i]) {
				if v == cur[i] {
					continue
				}
				trial := append([]float64(nil), cur...)
				trial[i] = v
				s := score(trial)
				a.Evals++
				if s > bestS+eps {
					bestV, bestS = v, s
				}
			}
			if bestV != cur[i] {
				cur[i], best, improved = bestV, bestS, true
			}
		}
		if !improved {
			break
		}
	}
	a.Values, a.Score = cur, best
	return a
}
