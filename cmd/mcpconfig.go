package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var mcpConfigAllow bool

const claudeAllowSnippet = "{\n  \"permissions\": {\n    \"allow\": [\"mcp__onfly\"]\n  }\n}"

var mcpConfigCmd = &cobra.Command{
	Use:       "mcp-config <claude-code|cursor|vscode>",
	Short:     "Print the MCP server entry for an IDE",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"claude-code", "cursor", "vscode"},
	RunE: func(cmd *cobra.Command, args []string) error {
		if mcpConfigAllow {
			if args[0] != "claude-code" {
				return fmt.Errorf("--allow is for claude-code, not %q", args[0])
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Merge this into .claude/settings.json (every onfly tool is read-only):")
			fmt.Fprintln(cmd.OutOrStdout(), claudeAllowSnippet)
			return nil
		}
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
	var b strings.Builder
	fmt.Fprintf(&b, "{\n  %q: {\n    \"onfly\": {\n", key)
	if vscode {
		b.WriteString("      \"type\": \"stdio\",\n")
	}
	fmt.Fprintf(&b, "      \"command\": \"onfly\",\n      \"args\": %s\n    }\n  }\n}", args)
	return file, []byte(b.String()), nil
}

func init() {
	mcpConfigCmd.Flags().BoolVar(&mcpConfigAllow, "allow", false, "print the Claude Code permission rule that allows every onfly tool, instead of the server entry")
}
