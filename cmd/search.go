package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	"github.com/spf13/cobra"
)

var (
	searchLimit     int
	searchWorkspace string
	searchJSON      bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search the index from the terminal",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		s, closeStore, err := query.Open(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		defer closeStore()
		chunks, err := s.Search(cmd.Context(), strings.Join(args, " "), searchLimit, query.Scope(cfg.Workspace, searchWorkspace))
		if err != nil {
			return err
		}
		return printHits(cmd.OutOrStdout(), chunks, searchJSON)
	},
}

func init() {
	searchCmd.Flags().IntVar(&searchLimit, "limit", query.DefaultLimit, "maximum results, from 1 to 50")
	searchCmd.Flags().StringVar(&searchWorkspace, "workspace", "", "workspace name, or * for every workspace")
	searchCmd.Flags().BoolVar(&searchJSON, "json", false, "print JSON with chunk content")
}

type hitRow struct {
	FilePath  string `json:"file_path"`
	Symbol    string `json:"symbol,omitempty"`
	Receiver  string `json:"receiver,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Content   string `json:"content"`
}

func rowOf(c parser.Chunk) hitRow {
	row := hitRow{FilePath: c.FilePath, Content: c.Content}
	row.Symbol = c.Metadata["name"]
	row.Receiver = c.Metadata["receiver"]
	row.StartLine, _ = strconv.Atoi(c.Metadata["start_line"])
	row.EndLine, _ = strconv.Atoi(c.Metadata["end_line"])
	return row
}

func printHits(w io.Writer, chunks []parser.Chunk, asJSON bool) error {
	rows := make([]hitRow, 0, len(chunks))
	for _, c := range chunks {
		rows = append(rows, rowOf(c))
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	for _, r := range rows {
		line := r.FilePath
		if r.StartLine > 0 {
			line += fmt.Sprintf(":%d-%d", r.StartLine, max(r.EndLine, r.StartLine))
		}
		if sym := strings.TrimPrefix(r.Receiver+"."+r.Symbol, "."); r.Symbol != "" {
			line += "  " + sym
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
