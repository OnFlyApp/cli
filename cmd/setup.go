package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup <claude>",
	Short: "Wire onfly into an assistant: register the MCP server and write the instructions",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "claude" {
			return fmt.Errorf("unknown assistant %q: only claude is supported", args[0])
		}
		serveArgs := []string{"serve"}
		if configPath != "" {
			abs, err := filepath.Abs(configPath)
			if err != nil {
				return err
			}
			serveArgs = append(serveArgs, "--config", abs)
		}
		if err := mergeMCPServer(".mcp.json", serveArgs); err != nil {
			return err
		}
		if err := writeGuide("CLAUDE.md"); err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "registered onfly in .mcp.json")
		fmt.Fprintln(out, "wrote the onfly block to CLAUDE.md")
		fmt.Fprintln(out, "to skip permission prompts, run: onfly mcp-config claude-code --allow")
		return nil
	},
}

// mergeMCPServer adds the onfly entry to an MCP config file and leaves every
// other server and key as it was.
func mergeMCPServer(file string, serveArgs []string) error {
	doc := map[string]any{}
	raw, err := os.ReadFile(file)
	switch {
	case err == nil && len(raw) > 0:
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s is not valid JSON: %w", file, err)
		}
	case err != nil && !os.IsNotExist(err):
		return err
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["onfly"] = map[string]any{"command": "onfly", "args": serveArgs}
	doc["mcpServers"] = servers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(out, '\n'), 0o644)
}

func init() {
	rootCmd.AddCommand(setupCmd)
}
