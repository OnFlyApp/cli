package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/axitdev/onfly/internal/envscan"
	"github.com/axitdev/onfly/internal/mcp"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/spf13/cobra"
)

var (
	envFormat string
	envFilter string
	envStrict bool
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "List the environment variables a project defines and reads",
	Long: "Reads .env, .env.example and similar files (names only, never values) and finds where the code reads\n" +
		"each variable (os.Getenv, env(), process.env, os.environ, env::var, ENV[...]). Reports the variables used but\n" +
		"defined in no env file, and the ones defined but never read.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if envFormat != "text" && envFormat != "json" {
			return fmt.Errorf("unknown --format %q: use text or json", envFormat)
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		st, err := vectorstore.New(cmd.Context(), cfg.Database)
		if err != nil {
			return err
		}
		defer st.Close()
		if err := vectorstore.InitAll(cmd.Context(), st, cfg); err != nil {
			return err
		}
		rep, err := envscan.Collect(cmd.Context(), cfg.ProjectRoot, st)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if envFormat == "json" {
			if err := json.NewEncoder(out).Encode(rep); err != nil {
				return err
			}
		} else {
			body, _ := mcp.RenderEnv(rep, envFilter, func(string) int { return 0 })
			fmt.Fprint(out, body)
		}
		if envStrict && len(rep.Missing) > 0 {
			return fmt.Errorf("%d variables are read in code but defined in no env file", len(rep.Missing))
		}
		return nil
	},
}

func init() {
	envCmd.Flags().StringVar(&envFormat, "format", "text", "text or json")
	envCmd.Flags().StringVar(&envFilter, "filter", "", "keep variables whose name contains this text")
	envCmd.Flags().BoolVar(&envStrict, "strict", false, "exit non-zero when a variable is used but defined in no env file")
}
