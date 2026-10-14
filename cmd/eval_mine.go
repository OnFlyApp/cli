package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/axitdev/onfly/internal/eval"
	"github.com/axitdev/onfly/internal/usage"
	"github.com/spf13/cobra"
)

var (
	evalMineUsage string
	evalMineOut   string
)

var evalMineCmd = &cobra.Command{
	Use:   "mine",
	Short: "Turn recorded assistant usage into eval cases",
	Long: "With server.usage_file set, the MCP server records each chunk an assistant opens after a search.\n" +
		"mine writes those as eval cases: the question is the query, the opened chunk is the expected hit.\n" +
		"Real questions measure search better than generated ones.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		src := evalMineUsage
		if src == "" {
			src = cfg.Server.UsageFile
		}
		if src == "" {
			return fmt.Errorf("no usage file: set server.usage_file in the config or pass --usage")
		}
		if !filepath.IsAbs(src) {
			src = filepath.Join(cfg.ProjectRoot, src)
		}
		events, err := usage.Load(src)
		if err != nil {
			return err
		}
		cases := eval.Mine(events)
		if len(cases) == 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "no usage recorded in %s yet; run the MCP server with server.usage_file set and search from your assistant\n", src)
			return nil
		}
		raw, err := eval.MarshalCases(cases)
		if err != nil {
			return err
		}
		out := evalMineOut
		if !filepath.IsAbs(out) {
			out = filepath.Join(cfg.ProjectRoot, out)
		}
		if err := os.WriteFile(out, raw, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %d cases from %d opens to %s\n", len(cases), len(events), out)
		return nil
	},
}

func init() {
	evalMineCmd.Flags().StringVar(&evalMineUsage, "usage", "", "usage file (default server.usage_file)")
	evalMineCmd.Flags().StringVar(&evalMineOut, "out", ".onfly/eval-mined.yaml", "cases file to write, relative to the project root")
	evalCmd.AddCommand(evalMineCmd)
}
