package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/axitdev/onfly/internal/index"
	"github.com/spf13/cobra"
)

var (
	force  bool
	initCI bool
)

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
  # embed_context: false           # embed the bare code; by default each chunk carries "path: / kind: / symbol:"; reindex after changing
  # summaries:                     # one-sentence description per chunk, embedded with the code (slower index)
  #   provider: ollama            # or openai: vLLM, mlx_lm.server, LM Studio, llama.cpp (set endpoint)
  #   model: qwen2.5-coder:7b     # qwen2.5-coder:3b indexes faster for a small loss
  #   concurrency: 8              # chunks in flight at once; raise for servers that batch (vLLM, mlx_lm)
  #   context: true               # tell the model which symbols of the file a chunk calls and sits next to

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

# reranker:                        # optional second pass over the top results
#   provider: voyage              # voyage, cohere or jina
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
		if !initCI {
			bootstrapProject(cmd.Context(), cmd.OutOrStdout(), wd)
			warmIndex(cmd.Context(), cmd.OutOrStdout())
		}
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
  2. Connect Claude Code: onfly setup claude
     (Cursor or VS Code: onfly mcp-config cursor, onfly mcp-config vscode)
  3. Try a search:       onfly search "your question"

init already built the name/text index, so xray and focus work offline.
With the embedder up, run onfly index to add semantic search.

A language parser is installed for each manifest found (go.mod, composer.json, ...).
If that failed, run: onfly ext install <language>

Check the setup any time with: onfly doctor
`)
}

// warmIndex builds the first index without the embedder, so init stays fast
// and works offline: xray, focus and search by name, text and path work at
// once. "onfly index" (or the MCP server, in the background) adds the vectors.
func warmIndex(ctx context.Context, w io.Writer) {
	cfg, err := loadConfig()
	if err == nil {
		var stats index.Stats
		stats, err = index.Run(ctx, cfg, nil, index.Options{Lexical: true})
		if err == nil {
			fmt.Fprintf(w, "indexed %d files, %d chunks (semantic vectors: run onfly index with your embedder up)\n", stats.Files, stats.Chunks)
			return
		}
	}
	fmt.Fprintf(w, "could not build the index yet (%v); run: onfly index\n", err)
}

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
