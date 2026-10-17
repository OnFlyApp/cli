package cmd

import (
	"os"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/logger"
	"github.com/axitdev/onfly/internal/mcp"
	"github.com/spf13/cobra"
)

var (
	configPath string
	verbose    bool
	jsonLogs   bool
)

// logOptions are the log flags every command shares. plain asks for the
// human-readable form when --json-logs is not set.
func logOptions(plain bool) logger.Options {
	return logger.Options{Verbose: verbose, JSON: jsonLogs, Plain: plain}
}

var rootCmd = &cobra.Command{
	Use:           "onfly",
	Short:         "Index a codebase and serve it over MCP",
	Long:          "Onfly indexes a project and serves search tools to an IDE.\n\nRun onfly index to build the vector index.\nRun onfly serve to speak MCP JSON-RPC on stdout.",
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       mcp.Version,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "path to config.yaml")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "log debug detail (to stderr, or to server.log_file for serve; never to stdout)")
	rootCmd.PersistentFlags().BoolVar(&jsonLogs, "json-logs", false, "write log records as one JSON object per line")
	rootCmd.AddCommand(initCmd, indexCmd, serveCmd, watchCmd, doctorCmd, optimizeCmd, extCmd, searchCmd, statsCmd, mcpConfigCmd, evalCmd, envCmd)
}

func loadConfig() (config.Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(wd, configPath)
}
