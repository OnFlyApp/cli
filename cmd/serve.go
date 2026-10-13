package cmd

import (
	"os"

	"github.com/axitdev/onfly/internal/mcp"
	"github.com/spf13/cobra"
)

var serveMaxTokens int

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve MCP tools on stdio",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if serveMaxTokens > 0 {
			cfg.Server.MaxTokens = serveMaxTokens
		}
		return mcp.Serve(cmd.Context(), cfg, os.Stdin, os.Stdout)
	},
}

func init() {
	serveCmd.Flags().IntVar(&serveMaxTokens, "max-tokens", 0, "override server.max_tokens for search results")
}
