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
	evalCases     string
	evalK         int
	evalMinRecall float64
	evalJSON      bool
)

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Score search results against expected files and symbols",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		path := evalCases
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.ProjectRoot, path)
		}
		cases, err := eval.Load(path)
		if err != nil {
			return err
		}
		s, closeStore, err := query.Open(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		defer closeStore()
		ws := query.Scope(cfg.Workspace, "")
		rep, err := eval.Run(cmd.Context(), cases, func(ctx context.Context, q string, limit int) ([]parser.Chunk, error) {
			return s.Search(ctx, q, limit, ws)
		}, evalK)
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
		return nil
	},
}

func init() {
	evalCmd.Flags().StringVar(&evalCases, "cases", ".onfly/eval.yaml", "path to the cases file, relative to the project root")
	evalCmd.Flags().IntVar(&evalK, "k", 10, "rank cutoff")
	evalCmd.Flags().Float64Var(&evalMinRecall, "min-recall", 0, "exit non-zero when recall is below this value")
	evalCmd.Flags().BoolVar(&evalJSON, "json", false, "print the report as JSON")
}
