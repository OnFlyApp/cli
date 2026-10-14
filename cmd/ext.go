package cmd

import (
	"os"

	"github.com/axitdev/onfly/internal/ext"
	"github.com/spf13/cobra"
)

var extYes bool
var extExtensions string

var extCmd = &cobra.Command{Use: "ext", Short: "Install and remove WASM parsers"}

var extInstallCmd = &cobra.Command{
	Use:  "install <plugin[@version]|path.wasm>",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		return ext.Install(cmd.Context(), extOptions(wd), args[0])
	},
}

func extOptions(dir string) ext.Options {
	tty := isTerminal(os.Stdin)
	return ext.Options{
		Dir: dir, Config: configPath, Yes: extYes, IsTTY: tty, Extensions: extExtensions,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	}
}

var extUpdateCmd = &cobra.Command{
	Use:  "update [plugin]",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return ext.Update(cmd.Context(), extOptions(wd), name)
	},
}

func init() {
	extCmd.PersistentFlags().BoolVar(&extYes, "yes", false, "overwrite parsers for the same extensions")
	extInstallCmd.Flags().StringVar(&extExtensions, "extensions", "", "comma-separated extensions for a local wasm file")
	extCmd.AddCommand(extInstallCmd, extUpdateCmd, extRemoveCmd, extListCmd)
}

var extRemoveCmd = &cobra.Command{
	Use:  "remove <plugin>",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		return ext.Remove(extOptions(wd), args[0])
	},
}

var extListCmd = &cobra.Command{
	Use:  "list",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		return ext.List(cmd.Context(), extOptions(wd))
	},
}
