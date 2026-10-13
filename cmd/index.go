package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/axitdev/onfly/internal/index"
	"github.com/spf13/cobra"
)

var (
	indexCI     bool
	indexDryRun bool
)

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Walk the project, embed chunks, and store them",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		stats, err := index.Run(cmd.Context(), cfg, logger, index.Options{CI: indexCI, DryRun: indexDryRun})
		if indexDryRun {
			usd, known := index.EstimateUSD(cfg.Embedder.Model, stats.Tokens)
			if !known {
				usd = 0
			}
			fmt.Printf("files: %d\nchunks: %d\ntokens: %d\nestimated_cost_usd: %.6f\n", stats.Files, stats.Chunks, stats.Tokens, usd)
			return err
		}
		fmt.Fprintf(os.Stderr, "indexed %d files, %d chunks, skipped %d, pruned %d, failed %d\n", stats.Files, stats.Chunks, stats.Skipped, stats.Pruned, stats.Failed)
		return err
	},
}

func init() {
	indexCmd.Flags().BoolVar(&indexCI, "ci", false, "embed 128 chunks per request")
	indexCmd.Flags().BoolVar(&indexDryRun, "dry-run", false, "parse and estimate cost without writing the index")
}
