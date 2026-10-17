package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/axitdev/onfly/internal/audit"
	"github.com/axitdev/onfly/internal/telemetry"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/spf13/cobra"
)

var (
	auditTranscripts string
	auditSinceDays   int
	auditFormat      string
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Report where an assistant's tokens go and how to spend fewer",
	Long: "Combines what the Onfly tools saved (telemetry), the sizes of the indexed files, and the assistant's own\n" +
		"session logs, because Onfly cannot see a Read or Grep it did not serve. Session logs are read from\n" +
		"~/.claude/projects/<project> (Claude Code) unless --transcripts names a directory of .jsonl files, or\n" +
		"--transcripts none skips them.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if auditFormat != "text" && auditFormat != "json" {
			return fmt.Errorf("unknown --format %q: use text or json", auditFormat)
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		var since time.Time
		if auditSinceDays > 0 {
			since = time.Now().AddDate(0, 0, -auditSinceDays)
		}
		in := audit.Inputs{Since: since, SizeOf: func(rel string) int {
			info, err := os.Stat(filepath.Join(cfg.ProjectRoot, filepath.FromSlash(rel)))
			if err != nil {
				return 0
			}
			return int(info.Size())
		}}
		if tp := cfg.Server.TelemetryPath(); tp != "" {
			if _, err := os.Stat(tp); err == nil {
				if tel, err := telemetry.Open(tp); err == nil {
					defer tel.Close()
					in.Telemetry = tel
				}
			}
		}
		if st, err := vectorstore.New(cmd.Context(), cfg.Database); err == nil {
			defer st.Close()
			if vectorstore.InitAll(cmd.Context(), st, cfg) == nil {
				in.Files, _ = st.ListFiles(cmd.Context())
			}
		}
		dir := auditTranscripts
		if dir == "" {
			dir = audit.ProjectDir(cfg.ProjectRoot)
		}
		if dir != "none" {
			if tr, err := audit.ReadTranscripts(dir, cfg.ProjectRoot, since); err == nil && tr.Sessions > 0 {
				in.Transcript = &tr
			}
		}
		rep, err := audit.Build(cmd.Context(), in)
		if err != nil {
			return err
		}
		if auditFormat == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rep)
		}
		fmt.Fprint(cmd.OutOrStdout(), rep.Text())
		return nil
	},
}

func init() {
	auditCmd.Flags().StringVar(&auditTranscripts, "transcripts", "", "directory of assistant session logs (.jsonl); default is Claude Code's folder for this project, \"none\" skips them")
	auditCmd.Flags().IntVar(&auditSinceDays, "since", 0, "only the last N days")
	auditCmd.Flags().StringVar(&auditFormat, "format", "text", "text or json")
}
