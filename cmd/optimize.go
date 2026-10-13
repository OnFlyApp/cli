package cmd

import (
	"fmt"
	"os"

	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/spf13/cobra"
)

var optimizeCmd = &cobra.Command{
	Use:   "optimize",
	Short: "Compact the index",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		store, err := vectorstore.New(ctx, cfg.Database)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.Init(ctx, cfg.Embedder.Dimensions); err != nil {
			return err
		}
		if err := store.Optimize(ctx); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "optimized")
		return nil
	},
}
