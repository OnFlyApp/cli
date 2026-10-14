package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

var mcpConfigCmd = &cobra.Command{
	Use:       "mcp-config <claude-code|cursor|vscode>",
	Short:     "Print the MCP server entry for an IDE",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"claude-code", "cursor", "vscode"},
	RunE: func(cmd *cobra.Command, args []string) error {
		serveArgs := []string{"serve"}
		if configPath != "" {
			abs, err := filepath.Abs(configPath)
			if err != nil {
				return err
			}
			serveArgs = append(serveArgs, "--config", abs)
		}
		file, body, err := mcpSnippet(args[0], serveArgs)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Add this to %s:\n", file)
		_, err = cmd.OutOrStdout().Write(append(body, '\n'))
		return err
	},
}

func mcpSnippet(client string, serveArgs []string) (string, []byte, error) {
	entry := map[string]any{"command": "onfly", "args": serveArgs}
	var file string
	var doc map[string]any
	switch client {
	case "claude-code":
		file, doc = ".mcp.json", map[string]any{"mcpServers": map[string]any{"onfly": entry}}
	case "cursor":
		file, doc = ".cursor/mcp.json", map[string]any{"mcpServers": map[string]any{"onfly": entry}}
	case "vscode":
		entry["type"] = "stdio"
		file, doc = ".vscode/mcp.json", map[string]any{"servers": map[string]any{"onfly": entry}}
	default:
		return "", nil, fmt.Errorf("unknown client %q: use claude-code, cursor or vscode", client)
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	return file, body, err
}
