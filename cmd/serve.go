package cmd

import (
	"fmt"
	"os"

	"github.com/axitdev/onfly/internal/mcp"
	"github.com/spf13/cobra"
)

var (
	serveMaxTokens int
	serveWatch     bool
)

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
		if serveWatch {
			cfg.Server.Watch = true
		}
		if isTerminal(os.Stdin) {
			fmt.Fprintln(os.Stderr, "onfly serve speaks MCP on stdin/stdout and is meant to be started by an IDE.")
			fmt.Fprintln(os.Stderr, "Run `onfly mcp-config claude-code` (or cursor, vscode) to get the entry to add. Waiting for a client; Ctrl-C to stop.")
		}
		// Logs go to server.log_file; stdout carries only JSON-RPC.
		return mcp.ServeWith(cmd.Context(), cfg, os.Stdin, os.Stdout, logOptions(false))
	},
}

func init() {
	serveCmd.Flags().IntVar(&serveMaxTokens, "max-tokens", 0, "override server.max_tokens for search results")
	serveCmd.Flags().BoolVar(&serveWatch, "watch", false, "reindex changed files while serving")
}
