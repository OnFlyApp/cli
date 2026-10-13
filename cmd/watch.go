package cmd

import (
	"log/slog"
	"os"

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
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		return watch.Run(cmd.Context(), cfg, logger)
	},
}
