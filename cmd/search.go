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
	searchChanged   string
	searchType      string
	searchPath      string
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
		chunks, err := s.Run(cmd.Context(), query.Request{
			Query: strings.Join(args, " "), Limit: searchLimit,
			Workspace: query.Scope(cfg.Workspace, searchWorkspace), ChangedSince: searchChanged, Type: searchType, Path: searchPath,
		})
		if err != nil {
			return err
		}
		if len(chunks) == 0 && !searchJSON {
			fmt.Fprintln(cmd.ErrOrStderr(), "no results. Is the index built? Run: onfly index")
		}
		return printHits(cmd.OutOrStdout(), chunks, searchJSON)
	},
}

func init() {
	searchCmd.Flags().IntVar(&searchLimit, "limit", query.DefaultLimit, "maximum results, from 1 to 50")
	searchCmd.Flags().StringVar(&searchWorkspace, "workspace", "", "workspace name, or * for every workspace")
	searchCmd.Flags().StringVar(&searchChanged, "changed-since", "", "git ref; keep only files that differ from it")
	searchCmd.Flags().StringVar(&searchType, "type", "", "keep one chunk type: function, method, struct, section (docs), ...")
	searchCmd.Flags().StringVar(&searchPath, "path", "", "keep files matching a glob, for example 'internal/**'")
	searchCmd.Flags().BoolVar(&searchJSON, "json", false, "print JSON with chunk content")
}

type hitRow struct {
	FilePath  string `json:"file_path"`
	Symbol    string `json:"symbol,omitempty"`
	Receiver  string `json:"receiver,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Content   string `json:"content"`
}

func rowOf(c parser.Chunk) hitRow {
	row := hitRow{FilePath: c.FilePath, Content: c.Content}
	row.Symbol = c.Metadata["name"]
	row.Receiver = c.Metadata["receiver"]
	row.Summary = c.Metadata["summary"]
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
		if r.Summary != "" {
			if _, err := fmt.Fprintf(w, "    %s\n", snippet(r.Summary)); err != nil {
				return err
			}
		} else if snip := snippet(r.Content); snip != "" {
			if _, err := fmt.Fprintf(w, "    %s\n", snip); err != nil {
				return err
			}
		}
	}
	return nil
}

func snippet(content string) string {
	for _, l := range strings.Split(content, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 100 {
				return string(r[:99]) + "…"
			}
			return l
		}
	}
	return ""
}
