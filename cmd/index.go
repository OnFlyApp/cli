package cmd

import (
	"fmt"
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
		progress := newIndexProgress(os.Stderr)
		stats, err := index.Run(cmd.Context(), cfg, progress.logger(), index.Options{
			CI: indexCI, DryRun: indexDryRun, Progress: progress.Event,
		})
		progress.Finish()
		if indexDryRun {
			fmt.Printf("files: %d\nchunks: %d\ntokens: %d\n", stats.Files, stats.Chunks, stats.Tokens)
			return err
		}
		fmt.Fprintf(os.Stderr, "indexed %d files, %d chunks, reused %d, skipped %d, pruned %d, failed %d\n", stats.Files, stats.Chunks, stats.Reused, stats.Skipped, stats.Pruned, stats.Failed)
		return err
	},
}

func init() {
	indexCmd.Flags().BoolVar(&indexCI, "ci", false, "embed 128 chunks per request")
	indexCmd.Flags().BoolVar(&indexDryRun, "dry-run", false, "parse and count tokens without writing the index")
}
