package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/axitdev/onfly/internal/mcp"
	"github.com/spf13/cobra"
)

const (
	guideStart = "<!-- onfly:start -->"
	guideEnd   = "<!-- onfly:end -->"
)

var (
	guideWrite bool
	guideFile  string
)

var guideCmd = &cobra.Command{
	Use:   "guide",
	Short: "Print, or write into CLAUDE.md, the text that tells an assistant when to use onfly",
	Long: "The same text is sent to MCP clients as server instructions. Writing it to CLAUDE.md (or AGENTS.md)\n" +
		"keeps it in front of the model even in clients that ignore server instructions. The block sits between\n" +
		guideStart + " and " + guideEnd + "; running the command again replaces it and touches nothing else.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !guideWrite {
			fmt.Fprintln(cmd.OutOrStdout(), mcp.Guidance)
			return nil
		}
		file := guideFile
		if file == "" {
			file = "CLAUDE.md"
		}
		if err := writeGuide(file); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote the onfly block to %s\n", file)
		return nil
	},
}

func writeGuide(file string) error {
	old, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", file, err)
	}
	block := guideStart + "\n" + mcp.Guidance + "\n" + guideEnd
	text := string(old)
	var out string
	i, j := strings.Index(text, guideStart), strings.Index(text, guideEnd)
	switch {
	case i >= 0 && j > i:
		out = text[:i] + block + text[j+len(guideEnd):]
	case text == "":
		out = block + "\n"
	default:
		out = strings.TrimRight(text, "\n") + "\n\n" + block + "\n"
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".onfly-guide-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", file, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func init() {
	guideCmd.Flags().BoolVar(&guideWrite, "write", false, "write the block into a file instead of printing it")
	guideCmd.Flags().StringVar(&guideFile, "file", "", "file for --write (default CLAUDE.md)")
	rootCmd.AddCommand(guideCmd)
}
