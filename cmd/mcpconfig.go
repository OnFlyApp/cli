package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

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
	var file, key string
	vscode := false
	switch client {
	case "claude-code":
		file, key = ".mcp.json", "mcpServers"
	case "cursor":
		file, key = ".cursor/mcp.json", "mcpServers"
	case "vscode":
		file, key, vscode = ".vscode/mcp.json", "servers", true
	default:
		return "", nil, fmt.Errorf("unknown client %q: use claude-code, cursor or vscode", client)
	}
	quoted := make([]string, len(serveArgs))
	for i, a := range serveArgs {
		raw, err := json.Marshal(a)
		if err != nil {
			return "", nil, err
		}
		quoted[i] = string(raw)
	}
	args := "[" + strings.Join(quoted, ", ") + "]"
	// Written by hand so the keys keep a readable order and args stay on one line.
	var b strings.Builder
	fmt.Fprintf(&b, "{\n  %q: {\n    \"onfly\": {\n", key)
	if vscode {
		b.WriteString("      \"type\": \"stdio\",\n")
	}
	fmt.Fprintf(&b, "      \"command\": \"onfly\",\n      \"args\": %s\n    }\n  }\n}", args)
	return file, []byte(b.String()), nil
}
