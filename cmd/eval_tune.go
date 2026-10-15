package cmd

import (
	"fmt"
	"io"

	"github.com/axitdev/onfly/internal/eval"
	"github.com/axitdev/onfly/internal/query"
	"github.com/spf13/cobra"
)

var (
	evalTuneTop int
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

func tuneGrid(cur query.Params) []tuneSetting {
	mk := func(p query.Params) tuneSetting {
		return tuneSetting{p, fmt.Sprintf("semantic=%g keyword=%g word=%g field=%g rrf_k=%d", p.Semantic, p.Keyword, p.Word, p.Field, p.RRFK)}
	}
	out := []tuneSetting{mk(cur)}
	for _, sem := range []float64{0.5, 1, 1.5, 2} {
		for _, kw := range []float64{0.5, 1, 1.5} {
			for _, word := range []float64{0, 0.1, 0.25, 0.5} {
				for _, field := range []float64{0, 0.5, 1, 2} {
					for _, k := range []int{20, 60} {
						p := cur
						p.Semantic, p.Keyword, p.Word, p.Field, p.RRFK = sem, kw, word, field, k
						out = append(out, mk(p))
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
	fmt.Fprintf(out, "\nbest setting gains %+.3f MRR on the check half. To adopt it:\n\nsearch:\n  semantic_weight: %g\n  keyword_weight: %g\n  word_weight: %g\n  field_weight: %g\n  rrf_k: %d\n",
		gain, best.Semantic, best.Keyword, best.Word, best.Field, best.RRFK)
}

func init() {
	evalTuneCmd.Flags().IntVar(&evalTuneTop, "top", 5, "settings to list")
	evalTuneCmd.Flags().IntVar(&evalK, "k", 10, "rank cutoff")
	evalTuneCmd.Flags().BoolVar(&evalFileLevel, "file-level", false, "count a hit when the file matches, whatever the symbol")
	evalCmd.AddCommand(evalTuneCmd)
}
