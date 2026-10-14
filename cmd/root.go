package cmd

import (
	"os"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/mcp"
	"github.com/spf13/cobra"
)

var configPath string

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
	rootCmd.AddCommand(initCmd, indexCmd, serveCmd, watchCmd, doctorCmd, optimizeCmd, extCmd, searchCmd, statsCmd, mcpConfigCmd, evalCmd)
}

func loadConfig() (config.Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(wd, configPath)
}
