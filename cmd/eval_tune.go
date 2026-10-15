package cmd

import (
	"fmt"
	"io"

	"github.com/axitdev/onfly/internal/eval"
	"github.com/axitdev/onfly/internal/query"
	"github.com/spf13/cobra"
)

var (
	evalTuneTop   int
	evalTuneLearn bool
)

var evalTuneCmd = &cobra.Command{
	Use:   "tune",
	Short: "Grid-search the fusion weights on half the cases and check them on the other half",
	Long: "Retrieval runs once per case; every weight setting then only re-fuses the saved lists, so the grid is cheap.\n" +
		"The best setting by MRR on the even cases is scored again on the odd cases. Adopt it only when the\n" +
		"odd-case score also beats your current settings.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkEvalK(evalK); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		cases, err := loadEvalCases(cfg.ProjectRoot, evalCases)
		if err != nil {
			return err
		}
		s, closeStore, err := query.Open(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		defer closeStore()
		ws := query.Scope(cfg.Workspace, "")
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "retrieving %d cases...\n", len(cases))
		gathered := make([]*query.Gathered, len(cases))
		for i, c := range cases {
			if gathered[i], err = s.Gather(cmd.Context(), query.Request{Query: c.Query, Limit: evalK, Workspace: ws}); err != nil {
				return fmt.Errorf("query %q: %w", c.Query, err)
			}
		}
		if evalTuneLearn {
			return learnWeights(out, s, cases, gathered)
		}
		settings := tuneGrid(s.Params())
		tuner := eval.Tuner{
			N:    len(settings),
			Name: func(i int) string { return settings[i].label },
			Rank: func(setting int, idx []int) ([]int, error) {
				ranks := make([]int, len(idx))
				for j, ix := range idx {
					got := s.Fuse(gathered[ix], settings[setting].p)
					ranks[j] = eval.Rank(cases[ix], got, evalK, evalFileLevel)
				}
				return ranks, nil
			},
		}
		res, err := eval.Tune(tuner, len(cases), evalTuneTop)
		if err != nil {
			return err
		}
		printTune(out, res, settings[res.Best].p, len(cases), evalK)
		return nil
	},
}

type tuneSetting struct {
	p     query.Params
	label string
}

// tuneGrid lists the settings to try; the first is the current configuration.
func tuneGrid(cur query.Params) []tuneSetting {
	mk := func(p query.Params) tuneSetting {
		return tuneSetting{p, fmt.Sprintf("semantic=%g keyword=%g word=%g field=%g graph=%g rrf_k=%d", p.Semantic, p.Keyword, p.Word, p.Field, p.Graph, p.RRFK)}
	}
	out := []tuneSetting{mk(cur)}
	for _, sem := range []float64{0.5, 1, 1.5, 2} {
		for _, kw := range []float64{0.5, 1, 1.5} {
			for _, word := range []float64{0, 0.1, 0.25, 0.5} {
				for _, field := range []float64{0, 0.5, 1, 2} {
					for _, k := range []int{20, 60} {
						for _, graph := range []float64{0, 0.1, 0.25} {
							p := cur
							p.Semantic, p.Keyword, p.Word, p.Field, p.Graph, p.RRFK = sem, kw, word, field, graph, k
							out = append(out, mk(p))
						}
					}
				}
			}
		}
	}
	return out
}

func printTune(out io.Writer, r eval.Result, best query.Params, n, k int) {
	fmt.Fprintf(out, "fit on %d cases, check on %d (k=%d)\n\n", len(r.FitIdx), len(r.Hold), k)
	fmt.Fprintf(out, "%-9s %-6s %-6s %-6s %-6s  %s\n", "", "fit R", "fit MRR", "chk R", "chk MRR", "setting")
	fmt.Fprintf(out, "%-9s %-6.2f %-7.3f %-6.2f %-7.3f  %s\n", "current", r.BaseFit.Recall, r.BaseFit.MRR, r.BaseCheck.Recall, r.BaseCheck.MRR, r.BaseFit.Name)
	fmt.Fprintf(out, "%-9s %-6.2f %-7.3f %-6.2f %-7.3f  %s\n", "best", r.Fit.Recall, r.Fit.MRR, r.Check.Recall, r.Check.MRR, r.Fit.Name)
	fmt.Fprintln(out, "\ntop settings on the fit half:")
	for _, c := range r.Table {
		fmt.Fprintf(out, "  mrr %.3f  recall %.2f  %s\n", c.MRR, c.Recall, c.Name)
	}
	gain := r.Check.MRR - r.BaseCheck.MRR
	if gain < 0.01 || r.Best == 0 {
		fmt.Fprintf(out, "\nno setting beats the current one on the check half by 0.01 MRR (%+.3f); keep your configuration.\n", gain)
		return
	}
	fmt.Fprintf(out, "\nbest setting gains %+.3f MRR on the check half. To adopt it:\n\nsearch:\n  semantic_weight: %g\n  keyword_weight: %g\n  word_weight: %g\n  field_weight: %g\n  graph_weight: %g\n  rrf_k: %d\n",
		gain, best.Semantic, best.Keyword, best.Word, best.Field, best.Graph, best.RRFK)
}

func init() {
	evalTuneCmd.Flags().IntVar(&evalTuneTop, "top", 5, "settings to list")
	evalTuneCmd.Flags().BoolVar(&evalTuneLearn, "learn", false, "coordinate ascent over every weight (including summary, alt, graph and docs-section factors) instead of the fixed grid")
	evalTuneCmd.Flags().IntVar(&evalK, "k", 10, "rank cutoff")
	evalTuneCmd.Flags().BoolVar(&evalFileLevel, "file-level", false, "count a hit when the file matches, whatever the symbol")
	evalCmd.AddCommand(evalTuneCmd)
}

// learnWeights fits every fusion weight by coordinate ascent on the even
// cases and checks the result on the odd ones.
func learnWeights(out io.Writer, s *query.Searcher, cases []eval.Case, gathered []*query.Gathered) error {
	cur := s.Params()
	names := []string{"semantic_weight", "keyword_weight", "word_weight", "field_weight", "summary_weight", "alt_weight", "summary_word_weight", "graph_weight", "section_weight", "rrf_k"}
	vec := func(p query.Params) []float64 {
		return []float64{p.Semantic, p.Keyword, p.Word, p.Field, p.Summary, p.Alt, p.SummaryWord, p.Graph, p.Section, float64(p.RRFK)}
	}
	params := func(v []float64) query.Params {
		p := cur
		p.Semantic, p.Keyword, p.Word, p.Field, p.Summary, p.Alt, p.SummaryWord, p.Graph, p.Section = v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7], v[8]
		p.RRFK = int(v[9] + 0.5)
		return p
	}
	fit, check := eval.SplitIdx(len(cases))
	mrr := func(v []float64, idx []int) (recall, m float64) {
		p := params(v)
		ranks := make([]int, len(idx))
		for j, ix := range idx {
			ranks[j] = eval.Rank(cases[ix], s.Fuse(gathered[ix], p), evalK, evalFileLevel)
		}
		return eval.Score(ranks)
	}
	candidates := func(i int, v float64) []float64 {
		switch i {
		case 9: // rrf_k
			return []float64{10, 20, 30, 40, 60, 100}
		case 8: // section factor
			return []float64{0.5, 0.7, 0.85, 1, 1.2, 1.5}
		}
		if v == 0 {
			return []float64{0.1, 0.25, 0.5, 1}
		}
		return []float64{0, v * 0.5, v * 0.7, v * 1.4, v * 2}
	}
	start := vec(cur)
	_, startFit := mrr(start, fit)
	res := eval.Ascend(start, 4, 1e-4, candidates, func(v []float64) float64 {
		_, m := mrr(v, fit)
		return m
	})
	rf, mf := mrr(res.Values, fit)
	rc, mc := mrr(res.Values, check)
	r0c, m0c := mrr(start, check)
	r0f, _ := mrr(start, fit)
	fmt.Fprintf(out, "fit on %d cases, check on %d (k=%d), %d evaluations\n\n", len(fit), len(check), evalK, res.Evals)
	fmt.Fprintf(out, "%-9s %-6s %-7s %-6s %-7s\n", "", "fit R", "fit MRR", "chk R", "chk MRR")
	fmt.Fprintf(out, "%-9s %-6.2f %-7.3f %-6.2f %-7.3f\n", "current", r0f, startFit, r0c, m0c)
	fmt.Fprintf(out, "%-9s %-6.2f %-7.3f %-6.2f %-7.3f\n\n", "learned", rf, mf, rc, mc)
	if mc-m0c < 0.01 {
		fmt.Fprintf(out, "the learned weights do not beat the current ones on the check half by 0.01 MRR (%+.3f); keep your configuration.\n", mc-m0c)
		return nil
	}
	fmt.Fprintf(out, "learned weights gain %+.3f MRR on the check half. To adopt them:\n\nsearch:\n", mc-m0c)
	for i, n := range names {
		if i < 9 && res.Values[i] != start[i] || i == 9 && res.Values[i] != start[i] {
			fmt.Fprintf(out, "  %s: %g\n", n, res.Values[i])
		}
	}
	return nil
}
