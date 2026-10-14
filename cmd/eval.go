package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/axitdev/onfly/internal/eval"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	"github.com/spf13/cobra"
)

var (
	evalCases     []string
	evalSplit     string
	evalFileLevel bool
	evalK         int
	evalMinRecall float64
	evalJSON      bool
	evalBaseline  string
	evalUpdate    bool
)

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Score search results against expected files and symbols",
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
		if cases, err = eval.Split(cases, evalSplit); err != nil {
			return err
		}
		s, closeStore, err := query.Open(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		defer closeStore()
		ws := query.Scope(cfg.Workspace, "")
		rep, err := eval.RunLevel(cmd.Context(), cases, func(ctx context.Context, q string, limit int) ([]parser.Chunk, error) {
			return s.Search(ctx, q, limit, ws)
		}, evalK, evalFileLevel)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if evalJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(rep); err != nil {
				return err
			}
		} else {
			for _, c := range rep.Cases {
				mark := fmt.Sprintf("rank %d", c.Rank)
				if c.Rank == 0 {
					mark = "miss"
				}
				fmt.Fprintf(out, "%-8s %s\n", mark, c.Query)
			}
			fmt.Fprintf(out, "recall@%d: %.2f (%d/%d)  mrr: %.3f\n", rep.K, rep.Recall, rep.Hits, rep.Total, rep.MRR)
		}
		if rep.Recall < evalMinRecall {
			return fmt.Errorf("recall %.2f is below --min-recall %.2f", rep.Recall, evalMinRecall)
		}
		if evalBaseline == "" {
			return nil
		}
		bpath := evalBaseline
		if !filepath.IsAbs(bpath) {
			bpath = filepath.Join(cfg.ProjectRoot, bpath)
		}
		if evalUpdate {
			return eval.SaveBaseline(bpath, rep)
		}
		return eval.CheckBaseline(bpath, rep)
	},
}

func init() {
	evalCmd.PersistentFlags().StringSliceVar(&evalCases, "cases", []string{".onfly/eval.yaml"}, "cases file, relative to the project root; repeat or comma-separate to combine several")
	evalCmd.Flags().StringVar(&evalSplit, "split", "", "score only the even or odd cases (the halves \"eval tune\" fits and checks on)")
	evalCmd.Flags().BoolVar(&evalFileLevel, "file-level", false, "count a hit when the file matches, whatever the symbol")
	evalCmd.Flags().IntVar(&evalK, "k", 10, "rank cutoff")
	evalCmd.Flags().Float64Var(&evalMinRecall, "min-recall", 0, "exit non-zero when recall is below this value")
	evalCmd.Flags().StringVar(&evalBaseline, "baseline", "", "baseline file, relative to the project root; exit non-zero when recall or mrr drops below it")
	evalCmd.Flags().BoolVar(&evalUpdate, "update-baseline", false, "write the current scores to --baseline instead of comparing")
	evalCmd.Flags().BoolVar(&evalJSON, "json", false, "print the report as JSON")
}

func loadEvalCases(root string, paths []string) ([]eval.Case, error) {
	abs := make([]string, len(paths))
	for i, p := range paths {
		abs[i] = p
		if !filepath.IsAbs(p) {
			abs[i] = filepath.Join(root, p)
		}
	}
	return eval.LoadAll(abs)
}

func checkEvalK(k int) error {
	if k < 1 || k > query.MaxLimit {
		return fmt.Errorf("--k must be between 1 and %d, got %d", query.MaxLimit, k)
	}
	return nil
}
