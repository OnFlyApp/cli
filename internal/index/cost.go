package index

func EstimateUSD(model string, tokens int) (float64, bool) {
	perMillion := map[string]float64{
		"text-embedding-3-small": 0.02,
		"text-embedding-3-large": 0.13,
		"text-embedding-ada-002": 0.10,
		"voyage-code-3":          0.18,
		"voyage-3-large":         0.18,
		"voyage-code-4":          0.12,
		"voyage-4-large":         0.12,
		"voyage-4":               0.06,
		"voyage-3.5":             0.06,
		"voyage-4-lite":          0.02,
		"voyage-3.5-lite":        0.02,
	}
	rate, ok := perMillion[model]
	if !ok {
		return 0, false
	}
	return rate * float64(tokens) / 1_000_000, true
}
