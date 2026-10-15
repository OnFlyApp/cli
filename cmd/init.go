package cmd

import (
	"fmt"
	"io"
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
  # summaries:                     # one-sentence description per chunk, embedded with the code (slower index)
  #   provider: ollama
  #   model: qwen2.5-coder:7b     # qwen2.5-coder:3b indexes faster for a small loss
  #   context: true               # tell the model which symbols of the file a chunk calls and sits next to
  # embed_neighbours: true         # add nearby symbol names to each embedded chunk

rules:
  - .onfly/rules/project.md

# workspace: backend               # label stored on each chunk, for monorepos with several configs

# embedder prompts and a second embedder, see docs/configuration.md:
#   embedder:
#     query_prefix: "task: code retrieval | query: "   # EmbeddingGemma
#     document_prefix: "title: none | text: "
#     alt: {provider: ollama, model: qwen3-embedding:0.6b, dimensions: 1024}

# search:                          # how result lists are fused; measure changes with "onfly eval"
#   semantic_weight: 2             # defaults shown; "onfly eval tune" finds numbers for your code
#   keyword_weight: 0.5
#   field_weight: 0.5              # symbol-name / path / comment list; 0 turns it off
#   group_by: chunk                # file: one result per file
#   recency_weight: 0              # >0 favours files changed in the last recency_days (git)
#   graph_weight: 0                # >0 favours hits that call or are called by other hits
#   usage_weight: 0                # >0 favours chunks an assistant opened after earlier searches (needs server.usage_file)
#   expand:                        # ask a chat model for extra keywords before searching
#     provider: ollama
#     model: qwen2.5-coder:7b

# reranker:                        # optional second pass over the top results
#   provider: voyage              # voyage, cohere, jina, or ollama (a chat model)
#   model: rerank-2

server:
  transport: stdio
  log_level: info
  log_file: .onfly/server.log
  # max_tokens: 8192               # budget for search results and rules
  # watch: true                    # reindex changed files while serving
  # usage_file: .onfly/usage.jsonl # record which hit an assistant opens; "onfly eval mine" turns it into eval cases
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
		created, err := initProject(wd, force, initCI)
		if err != nil {
			return err
		}
		printInitReport(cmd.OutOrStdout(), created, initCI)
		return nil
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
	_, err := initProject(dir, overwrite, ci)
	return err
}

// initProject writes the files and returns their paths relative to dir.
func initProject(dir string, overwrite, ci bool) ([]string, error) {
	onfly := filepath.Join(dir, ".onfly")
	cfgPath := filepath.Join(onfly, "config.yaml")
	if _, err := os.Stat(cfgPath); err == nil && ci {
		if err := writeWorkflow(dir, overwrite); err != nil {
			return nil, err
		}
		return []string{workflowRel}, nil
	}
	if !overwrite {
		if _, err := os.Stat(cfgPath); err == nil {
			return nil, fmt.Errorf(".onfly/config.yaml already exists (use --force to overwrite it)")
		}
	}
	if err := os.MkdirAll(filepath.Join(onfly, "plugins"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(onfly, "rules"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(configTemplate, strconv.Quote(projectName(dir)))), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(onfly, "rules", "project.md"), []byte(projectRule), 0o644); err != nil {
		return nil, err
	}
	created := []string{".onfly/config.yaml", ".onfly/rules/project.md", ".onfly/plugins/"}
	if ci {
		if err := writeWorkflow(dir, overwrite); err != nil {
			return nil, err
		}
		created = append(created, workflowRel)
	}
	return created, nil
}

const workflowRel = ".github/workflows/onfly-index.yml"

func printInitReport(w io.Writer, created []string, ci bool) {
	for _, p := range created {
		fmt.Fprintf(w, "created %s\n", p)
	}
	if len(created) == 1 && created[0] == workflowRel {
		fmt.Fprintln(w, "\nCommit the workflow to index on every push to main.")
		return
	}
	fmt.Fprint(w, `
Next steps:
  1. Start your embedder: ollama pull qwen3-embedding:0.6b
     (or edit "embedder:" in .onfly/config.yaml)
  2. Optional, for code: onfly ext install go
  3. Build the index:    onfly index
  4. Try it:             onfly search "your question"
  5. Connect your IDE:   onfly mcp-config claude-code > .mcp.json

Check the setup any time with: onfly doctor
`)
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
	path := filepath.Join(dir, filepath.FromSlash(workflowRel))
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
