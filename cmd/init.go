package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
)

var (
	force  bool
	initCI bool
)

// configTemplate is the config `onfly init` writes. %s is the quoted project name.
// Optional blocks are commented out; docs/configuration.md describes every key.
const configTemplate = `project: %s

database:
  driver: sqlite                   # sqlite or postgres
  dsn: .onfly/vector.db            # postgres: postgres://user:${ONFLY_DB_PASSWORD}@host:5432/onfly

embedder:
  provider: ollama                 # ollama, openai, or voyage
  endpoint: http://localhost:11434
  model: qwen3-embedding:0.6b
  dimensions: 1024                 # must match the model; changing it needs a fresh index
  # api_key: ${OPENAI_API_KEY}     # openai and voyage also read OPENAI_API_KEY / VOYAGE_API_KEY

indexer:
  # Which files are candidates. A file is indexed when it matches include, is not
  # ignored (.gitignore and exclude) and a parser below handles its extension.
  include: ["**/*"]
  exclude: [vendor, node_modules, dist, build, target, .venv]
  parsers:
    # Built in, no download: Markdown and text, split into sections.
    - extensions: [.md, .txt]
      type: text
    # Language parsers: run "onfly ext list", then for example "onfly ext install go".
    # Each parser can be limited to part of the project:
    # - extensions: [.go]
    #   type: wasm
    #   path: .onfly/plugins/go-parser.wasm
    #   include: ["cmd/**", "internal/**"]
    #   exclude: ["**/*_test.go"]
  # concurrency: 4                 # files indexed in parallel
  # embed_context: true            # embed "path: / symbol:" with each chunk; reindex after changing

rules:
  - .onfly/rules/project.md

# workspace: backend               # label stored on each chunk, for monorepos with several configs

# search:                          # how result lists are fused; measure changes with "onfly eval"
#   keyword_weight: 1
#   semantic_weight: 1
#   recency_weight: 0              # >0 favours files changed in the last recency_days (git)

# reranker:                        # optional second pass over the top results
#   provider: voyage              # voyage, cohere, jina, or ollama (a chat model)
#   model: rerank-2

server:
  transport: stdio
  log_level: info
  log_file: .onfly/server.log
  # max_tokens: 8192               # budget for search results and rules
  # watch: true                    # reindex changed files while serving
`

const projectRule = `---
name: Project
scope: ["*"]
priority: 10
---
# {{ .Project.Name }}

Describe what this repository is and the conventions an assistant must follow.
Rules can be scoped to files, for example scope: ["**/*.go"], and each rule file
is a separate Markdown file listed under ` + "`rules`" + ` in config.yaml.
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
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(configTemplate, strconv.Quote(projectName(dir)))), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(onfly, "rules", "project.md"), []byte(projectRule), 0o644); err != nil {
		return err
	}
	if ci {
		return writeWorkflow(dir, overwrite)
	}
	return nil
}

// projectName is the directory name, which is what a person calls the project.
func projectName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if name := filepath.Base(abs); name != "" && name != "." && name != string(filepath.Separator) {
		return name
	}
	return "project"
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
