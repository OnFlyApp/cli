package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	force  bool
	initCI bool
)

const defaultConfig = `project: "E-commerce CRM"

database:
  driver: "sqlite"
  dsn: ".onfly/vector.db"

embedder:
  provider: "ollama"
  endpoint: "http://localhost:11434"
  model: "qwen3-embedding:0.6b"
  dimensions: 1024

rules:
  - ".onfly/rules/backend_style.md"
  - ".onfly/rules/frontend_style.md"
  - ".onfly/rules/architecture.md"

rules_engine:
  max_token_budget_percent: 15
  auto_discovery:
    enabled: true
    fallback_order:
      - ".cursorrules"
      - "AGENTS.md"
      - "CLAUDE.md"
      - "AGENT.md"
      - ".clinerules"
      - ".github/copilot-instructions.md"

indexer:
  include: ["**/*.go", "src/**/*", "cmd/**/*", "internal/**/*", "pkg/**/*", "assets/**/*", "docs/**/*.md"]
  exclude: ["vendor", "node_modules", "var", "public"]
  parsers: []

server:
  transport: "stdio"
  log_level: "debug"
  log_file: ".onfly/server.log"
`

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create .onfly/ with a default config",
	RunE: func(cmd *cobra.Command, args []string) error {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		return runInit(wd, force, initCI)
	},
}

func init() {
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	initCmd.Flags().BoolVar(&initCI, "ci", false, "write a GitHub Actions workflow")
}

const workflowYAML = `name: Onfly Global Indexer
on:
  push:
    branches: ["main"]
jobs:
  index:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install Onfly
        env:
          GOPRIVATE: github.com/axitdev/onfly
        run: go install github.com/axitdev/onfly@latest
      - name: Run Incremental Index
        run: onfly index --ci
`

func runInit(dir string, overwrite, ci bool) error {
	onfly := filepath.Join(dir, ".onfly")
	cfgPath := filepath.Join(onfly, "config.yaml")
	if _, err := os.Stat(cfgPath); err == nil && ci {
		if err := writeWorkflow(dir, overwrite); err != nil {
			return err
		}
		return nil
	}
	if !overwrite {
		if _, err := os.Stat(cfgPath); err == nil {
			return fmt.Errorf(".onfly/config.yaml already exists")
		}
	}
	if err := os.MkdirAll(filepath.Join(onfly, "plugins"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(onfly, "rules"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, []byte(defaultConfig), 0o644); err != nil {
		return err
	}
	rules := map[string]string{
		"backend_style.md":  "---\nname: Backend\nscope: [\"**/*.go\", \"**/*.php\", \"**/*.rs\"]\nignore: [\"**/*_test.go\", \"vendor/**\"]\npriority: 50\n---\n# Backend\n\nReturn errors to the caller. Keep request handlers thin.\n",
		"frontend_style.md": "---\nname: Frontend\nscope: [\"**/*.ts\", \"**/*.tsx\", \"**/*.js\"]\npriority: 40\n---\n# Frontend\n\nKeep components focused on one view.\n",
		"architecture.md":   "---\nname: Architecture\nscope: [\"*\"]\npriority: 10\n---\n# Architecture\n\nIndex source with Onfly. Serve search over MCP stdio.\nYou are assisting in the {{ .Project.Name }} repository.\n",
	}
	for name, body := range rules {
		if err := os.WriteFile(filepath.Join(onfly, "rules", name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	if ci {
		return writeWorkflow(dir, overwrite)
	}
	return nil
}

func writeWorkflow(dir string, overwrite bool) error {
	path := filepath.Join(dir, ".github", "workflows", "onfly-index.yml")
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(workflowYAML), 0o644)
}
