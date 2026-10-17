package cmd

import (
	"fmt"
	"os"

	"github.com/axitdev/onfly/internal/logger"
	"github.com/axitdev/onfly/internal/watch"
	"github.com/spf13/cobra"
)

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Reindex files as they change",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		logger := logger.New(os.Stderr, logOptions(true))
		fmt.Fprintf(os.Stderr, "watching %s for changes. Ctrl-C to stop.\n", cfg.ProjectRoot)
		return watch.Run(cmd.Context(), cfg, logger)
	},
}
