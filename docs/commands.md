# Commands

Every command reads `.onfly/config.yaml`. It is found by walking up from the current directory; `--config path` skips the search.

| Command | What it does |
| --- | --- |
| `onfly init` | Create `.onfly/`, a commented starter config, and one rule file (`.onfly/rules/project.md`). The project name is the directory name. |
| `onfly init --force` | Overwrite that config and those rule files. Without it, an existing config is an error that names the flag. |
| `onfly init --ci` | Also write `.github/workflows/onfly-index.yml`. An existing config is left in place. `--force` overwrites the workflow. |
| `onfly index` | Parse matching files, embed the chunks, and update the database. |
| `onfly index --dry-run` | Parse and print file, chunk, and token counts. Does not open the database or call the embedder. |
| `onfly index --ci` | Embed 128 chunks per request. The default batch is 32. |
| `onfly watch` | Reindex changed files after `indexer.debounce_ms`. Reloads ignore rules when a `.gitignore` changes. |
| `onfly serve` | Serve the MCP tools on stdio. |
| `onfly serve --watch` | Serve and reindex changed files in the same process. Same as `server.watch: true`. |
| `onfly serve --max-tokens N` | Cap search results at N tokens. `N` of 0 keeps `server.max_tokens`. |
| `onfly search "question"` | Search from the terminal. `--limit N`, `--workspace NAME`, `--changed-since REF`, `--type function|method|section|...`, `--path 'internal/**'`, `--group-by file|chunk`, `--json`. |
| `onfly doctor` | Check config, the database, the embedder, parser files, and rule frontmatter. Also prints the `stats` hints. |
| `onfly eval` | Score search against `.onfly/eval.yaml` (recall@k, MRR). `--cases A,B` combines files, `--split even|odd`, `--file-level`, `--explain`, `--judge MODEL`. See [Evaluation](evaluation.md). |
| `onfly eval tune` | Grid-search the fusion weights on half the cases and check them on the other half. `--learn` fits every weight by coordinate ascent instead. |
| `onfly eval mine` | Turn the recorded usage file (`server.usage_file`) into eval cases. |
| `onfly stats` | Print file and chunk counts, with breakdowns by chunk type, workspace, and extension. Prints `hint:` lines when data or generated files (`.json`, `.lock`, `.svg` and similar) take 15% or more of the indexed text. |
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

