# Onfly

Onfly indexes a codebase and serves that index to an IDE over the Model Context Protocol (MCP) on stdin and stdout.

Parsers are separate programs. A WASM module (sandboxed) or a shell command (`type: stdio`, which must be enabled with `ONFLY_ALLOW_STDIO=1`) reads one source file and returns JSON chunks. Onfly embeds those chunks and stores them in SQLite (`sqlite-vec` and FTS5) or Postgres (`pgvector`). `onfly serve` answers search and file tools, and attaches project rules from `.onfly/rules/` to those results. It does not write logs to stdout.

Operator guides are under [docs/](docs/).

## Requirements

- Go 1.27 or newer
- An embedder: [Ollama](https://ollama.com) running locally, an OpenAI API key, or a Voyage API key
- A parser for each extension you index. This repo does not ship language parsers. The Go parser is [OnFlyApp/ext-parser-go](https://github.com/OnFlyApp/ext-parser-go). `onfly ext install go` installs a published release.

## Build

```sh
make build
```

The binary is `bin/onfly`. The build sets `CGO_ENABLED=0` and stamps the version from `git describe`; `onfly --version` prints it.

## Index a Go project

Install a 1024-dimension model if you use the sample settings:

```sh
ollama pull qwen3-embedding:0.6b
```

In the target project:

```sh
onfly init
onfly ext install go
```

`onfly ext install go` downloads `go-parser.wasm` from [OnFlyApp/registry](https://github.com/OnFlyApp/registry), checks its SHA-256, and adds a `.go` parser. To build that parser yourself instead:

```sh
git clone https://github.com/OnFlyApp/ext-parser-go.git
cd ext-parser-go
make wasm
```

`make wasm` writes `bin/go-parser.wasm`. From the project that contains `.onfly/config.yaml`:

```sh
onfly ext install /path/to/ext-parser-go/bin/go-parser.wasm --extensions=".go"
```

`onfly init` writes an empty `parsers` list. An existing config file is left in place. `onfly ext install go` adds the Go parser. A project that should index only Go can use:

```yaml
project: demo

database:
  driver: sqlite
  dsn: .onfly/vector.db

embedder:
  provider: ollama
  endpoint: http://localhost:11434
  model: qwen3-embedding:0.6b
  dimensions: 1024

rules:
  - .onfly/rules/architecture.md

rules_engine:
  max_token_budget_percent: 15
  auto_discovery:
    enabled: true
    fallback_order:
      - .cursorrules
      - AGENTS.md
      - CLAUDE.md
      - AGENT.md
      - .clinerules
      - .github/copilot-instructions.md

indexer:
  include: ["**/*.go"]
  exclude: ["vendor"]
  parsers:
    - extensions: [".go"]
      type: wasm
      path: .onfly/plugins/go-parser.wasm

server:
  transport: stdio
  log_level: info
  log_file: .onfly/server.log
```

Then:

```sh
onfly index
onfly serve
```

`onfly index` prints the current step on stderr, then a one-line summary. On a terminal that step stays on one line (`parsing`, `embedding`, `unchanged`, then `pruning` and `linking references`). It exits 1 if any file failed to parse. An unchanged file is not parsed again. `onfly serve` speaks JSON-RPC on stdout. Logs go to `.onfly/server.log`.

Query the index from the terminal with `onfly search` (`--limit`, `--workspace`, `--json`):

```sh
onfly search "where is the file hash computed"
# internal/index/index.go:62-64  ContentHash
```

Point the IDE at the project directory, or pass `--config` with the absolute path of `config.yaml`. The client config is in [docs/mcp.md](docs/mcp.md).

## Connect an IDE

```bash
onfly mcp-config cursor > .cursor/mcp.json
```

Clients: `claude-code` (`.mcp.json`), `cursor` (`.cursor/mcp.json`), `vscode` (`.vscode/mcp.json`). With `--config`, the absolute config path is written into the entry.

## Commands

| Command | What it does |
| --- | --- |
| `onfly init` | Create `.onfly/`, a sample config with `rules_engine`, and three rule files. |
| `onfly init --force` | Overwrite that config and those rule files. |
| `onfly init --ci` | Also write `.github/workflows/onfly-index.yml`. An existing config is left in place. `--force` overwrites the workflow. |
| `onfly index` | Parse matching files, embed the chunks, and update the database. |
| `onfly index --dry-run` | Parse and print file, chunk, and token counts. Does not open the database or call the embedder. |
| `onfly index --ci` | Embed 128 chunks per request. The default batch is 32. |
| `onfly watch` | Reindex changed files after `indexer.debounce_ms`. Reloads ignore rules when a `.gitignore` changes. |
| `onfly serve` | Serve the MCP tools on stdio. |
| `onfly serve --max-tokens N` | Cap search results at N tokens. `N` of 0 keeps `server.max_tokens`. |
| `onfly doctor` | Check config, the database, the embedder, parser files, and rule frontmatter. |
| `onfly stats` | Print file and chunk counts, with breakdowns by chunk type and workspace. |
| `onfly mcp-config <claude-code\|cursor\|vscode>` | Print the MCP server entry for that IDE. JSON goes to stdout, the target file name to stderr. |
| `onfly optimize` | Compact the SQLite FTS index or reindex the Postgres indexes. |
| `onfly ext install go` | Download the published Go parser, check SHA-256, and add it to the config. |
| `onfly ext install go@1.0.0` | Install that published version. |
| `onfly ext install ./parser.wasm --extensions=".twig,.html"` | Install a local wasm file without the registry. |
| `onfly ext install ... --yes` | Replace a parser that already owns one of the extensions, without asking. |
| `onfly ext update` | Upgrade installed registry parsers. |
| `onfly ext remove go` | Remove the wasm file and its config entry. |
| `onfly ext list` | Show name, version, and `active`, `outdated`, or `corrupted`. |
| `--config path` | Use this config file instead of walking parent directories. |
| `--version` | Print the build version. |

`onfly ext` reads `https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json`, or the URL in `ONFLY_REGISTRY_URL`. The registry manifest is limited to 1 MiB, and a warning is printed when `ONFLY_REGISTRY_URL` overrides the default. A downloaded file is kept only when its SHA-256 matches the registry. A mismatch deletes the temp file and leaves the project unchanged. Installed plugins are recorded in `.onfly/plugins/installed.json`. `onfly ext list` waits at most three seconds for the registry, then prints the table. A failed fetch adds a `registry:` line.

## Rules

`onfly init` includes `**/*.go`, `src/**/*`, `cmd/**/*`, `internal/**/*`, `pkg/**/*`, `assets/**/*`, and `docs/**/*.md`. `parsers` stays empty until `onfly ext install`.

`onfly init` writes `.onfly/rules/backend_style.md`, `frontend_style.md`, and `architecture.md`. A rule file can start with YAML frontmatter:

```markdown
---
name: Database Safety Rules
scope: ["**/*.go", "interface"]
ignore: ["**/*_test.go"]
priority: 100
---
Never concatenate SQL in {{ .Project.Name }}. Active parsers: {{ .ActiveParsers }}.
```

`scope` takes a doublestar glob, a language name (`go`, `php`, `rust`, `typescript`, `javascript`, `python`, `ruby`, `java`), or a chunk type such as `interface`. `*` applies to every MCP call. A file with no frontmatter does too. Higher `priority` is sent first. `ignore` uses the same glob syntax.

`{{ .Project.Name }}` comes from `go.mod`, then `composer.json`, then `package.json`, then the `project` field. `{{ .Env.OS }}` and `{{ .Env.Arch }}` are the host system. `{{ .ActiveParsers }}` is a JSON list of languages taken from the configured parser extensions.

Injected rules use at most `rules_engine.max_token_budget_percent` (default 15) of `server.max_tokens`. Lower-priority rules are dropped. `onfly doctor` fails when frontmatter or a template cannot be read. When the rules are larger than that budget it prints `rules: [WARNING]` and exits 0 if the other checks passed.

With no file in `.onfly/rules/` and an empty `rules` list, serve reads the first existing path in `auto_discovery.fallback_order`. The default order starts `.cursorrules`, `AGENTS.md`, `CLAUDE.md`, `AGENT.md`. Set `auto_discovery.enabled` to false to skip those files. Global rules are in the server instructions. Matching rules are also a `rules` field on the tool result. The field list is in [docs/configuration.md](docs/configuration.md).

## Guides

- [Configuration](docs/configuration.md)
- [Parsers](docs/parsers.md)
- [Indexing](docs/indexing.md)
- [MCP server](docs/mcp.md)
- [Evaluation](docs/evaluation.md)
- [Development](docs/development.md)
