package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/axitdev/onfly/internal/gitx"
	"github.com/axitdev/onfly/internal/lint"
	"github.com/axitdev/onfly/internal/rules"
	"github.com/spf13/cobra"
)

var (
	lintBase   string
	lintStaged bool
	lintAll    bool
	lintFormat string
	lintStrict bool
)

var lintCmd = &cobra.Command{
	Use:   "lint [paths...]",
	Short: "Check a change against the forbidden_calls and forbidden_imports in the rules",
	Long: "Reads the rules in .onfly/rules and reports the lines a change adds that call or import something a rule\n" +
		"forbids. By default it checks the working tree against HEAD; --staged checks the index (for a pre-commit\n" +
		"hook), --base REF checks against another ref, and --all checks whole tracked files. Exit status 1 when an\n" +
		"error-level rule is violated (warnings too with --strict).",
	RunE: func(cmd *cobra.Command, args []string) error {
		if lintFormat != "text" && lintFormat != "json" {
			return fmt.Errorf("unknown --format %q: use text or json", lintFormat)
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		constraints := rules.Load(cfg).Constraints()
		var files []lint.File
		if lintAll {
			tracked, err := gitx.Tracked(cmd.Context(), cfg.ProjectRoot)
			if err != nil {
				return err
			}
			for _, rel := range tracked {
				raw, err := os.ReadFile(filepath.Join(cfg.ProjectRoot, rel))
				if err != nil || len(raw) > 1<<20 {
					continue
				}
				files = append(files, lint.WholeFile(rel, string(raw)))
			}
		} else {
			diff, err := gitx.Diff(cmd.Context(), cfg.ProjectRoot, lintBase, lintStaged)
			if err != nil {
				return err
			}
			files = lint.ParseDiff(diff)
		}
		if len(args) > 0 {
			files = lint.OnlyPaths(files, args)
		}
		found := lint.Check(constraints, files)
		out := cmd.OutOrStdout()
		if lintFormat == "json" {
			if found == nil {
				found = []lint.Violation{}
			}
			if err := json.NewEncoder(out).Encode(found); err != nil {
				return err
			}
		} else {
			for _, v := range found {
				fmt.Fprintf(out, "%s:%d: %s: %s [%s] %s\n", v.File, v.Line, v.Severity, v.Message, v.Rule, v.Match)
			}
			if len(constraints) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "no rule has forbidden_calls or forbidden_imports; nothing to check")
			}
		}
		errors, warnings := 0, 0
		for _, v := range found {
			if v.Severity == "warning" {
				warnings++
			} else {
				errors++
			}
		}
		if errors > 0 || lintStrict && warnings > 0 {
			return fmt.Errorf("lint: %d errors, %d warnings", errors, warnings)
		}
		return nil
	},
}

func init() {
	lintCmd.Flags().StringVar(&lintBase, "base", "", "compare the working tree with this git ref (default HEAD)")
	lintCmd.Flags().BoolVar(&lintStaged, "staged", false, "check the staged changes (pre-commit)")
	lintCmd.Flags().BoolVar(&lintAll, "all", false, "check every line of the tracked files, not just a change")
	lintCmd.Flags().StringVar(&lintFormat, "format", "text", "text or json")
	lintCmd.Flags().BoolVar(&lintStrict, "strict", false, "exit non-zero on warnings too")
}
