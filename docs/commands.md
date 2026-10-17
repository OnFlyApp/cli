# Commands

Every command reads `.onfly/config.yaml`. It is found by walking up from the current directory; `--config path` skips the search.

| Command | What it does |
| --- | --- |
| `onfly init` | Create `.onfly/`, a commented starter config, and one rule file (`.onfly/rules/project.md`). The project name is the directory name. |
| `onfly init` (continued) | Outside `--ci` it also adds `.onfly/` to `.gitignore` (when the project is a git repository or already has a `.gitignore`), and for each language found from a manifest (`go.mod` Go, `composer.json` PHP, `package.json` TypeScript, `Cargo.toml` Rust, `pyproject.toml` and `requirements.txt` Python, `Gemfile` Ruby, `pom.xml` and `build.gradle` Java) tries `onfly ext install`. A failed install prints the command to run by hand and does not fail `init`. |
| `onfly init` (index) | Outside `--ci` it ends by indexing the project without the embedder, so it takes seconds and works offline. If that fails it prints the reason and `onfly index` to run. |
| `onfly init --force` | Overwrite that config and those rule files. Without it, an existing config is an error that names the flag. |
| `onfly init --ci` | Also write `.github/workflows/onfly-index.yml`. An existing config is left in place. `--force` overwrites the workflow. |
| `onfly index` | Parse matching files, embed the chunks, and update the database. |
| `onfly index --lexical` | Store names, text and paths without calling the embedder, using every core to parse. A later `onfly index` adds the vectors. See [Indexing](indexing.md#without-the-embedder). |
| `onfly index --dry-run` | Parse and print file, chunk, and token counts. Does not open the database or call the embedder. |
| `onfly index --ci` | Embed 128 chunks per request. The default batch is 32. |
| `onfly watch` | Reindex changed files after `indexer.debounce_ms`. Reloads ignore rules when a `.gitignore` changes. |
| `onfly serve` | Serve the MCP tools on stdio. |
| `onfly serve --watch` | Serve and reindex changed files in the same process. Same as `server.watch: true`. |
| `onfly serve --max-tokens N` | Cap search results at N tokens. `N` of 0 keeps `server.max_tokens`. |
| `onfly search "question"` | Search from the terminal. `--limit N`, `--workspace NAME`, `--changed-since REF`, `--type function|method|section|...`, `--path 'internal/**'`, `--json`. |
| `onfly doctor` | Check config, the database, the embedder, parser files, and rule frontmatter. Also prints the `stats` hints. |
| `onfly eval` | Score search against `.onfly/eval.yaml` (recall@k, MRR). `--cases A,B` combines files, `--split even|odd`, `--file-level`, `--explain`, `--judge MODEL`. See [Evaluation](evaluation.md). |
| `onfly eval tune` | Grid-search the fusion weights on half the cases and check them on the other half. |
| `onfly eval mine` | Turn the recorded usage file (`server.usage_file`) into eval cases. |
| `onfly stats --format json` | Print only the telemetry totals as one JSON object, for CI: `requests`, `tokens_saved`, `ratio`, `usd_saved`, `rules_injected`. Zeros when no tool has run yet. |
| `onfly stats` | Print file and chunk counts, with breakdowns by chunk type, workspace, and extension. Prints `hint:` lines when data or generated files (`.json`, `.lock`, `.svg` and similar) take 15% or more of the indexed text, when the embedder reads fewer tokens than `embedder.max_input_tokens`, and when chunks are longer than about 2000 tokens. The type list is what `indexer.skip_types` and `search.type_weights` take. When `onfly_xray` or `onfly_focus` has been called, it ends with a telemetry block: requests served, tokens saved and the share they represent, dollars at $3 per million input tokens, and rules injected; see [MCP server](mcp.md#telemetry). |
| `onfly setup claude` | Register the server in `.mcp.json` (keeping its other servers) and write the usage guide into `CLAUDE.md` between the `onfly` markers. Run again to refresh both. `--config` is written into the server entry. Add `onfly mcp-config claude-code --allow` for the permission rule. |
| `onfly guide` | Print the tool-routing guidance sent to MCP clients. `--write` puts it into `CLAUDE.md` (or `--file PATH`) between `<!-- onfly:start -->` markers; run again to replace it. |
| `onfly mcp-config <claude-code\|cursor\|vscode>` | Print the MCP server entry for that IDE. JSON goes to stdout, the target file name to stderr. `--allow` (Claude Code) prints the permission rule that lets every onfly tool run without a prompt. |
| `onfly optimize` | Compact the SQLite FTS index or reindex the Postgres indexes. |
| `onfly ext install go` | Download the published Go parser, check SHA-256, and add it to the config. |
| `onfly ext install go@1.0.0` | Install that published version. |
| `onfly ext install ./parser.wasm --extensions=".twig,.html"` | Install a local wasm file without the registry. |
| `onfly ext install ... --yes` | Replace a parser that already owns one of the extensions, without asking. |
| `onfly ext update` | Upgrade installed registry parsers. |
| `onfly ext remove go` | Remove the wasm file and its config entry. |
| `onfly ext list` | Show name, version, and `active`, `outdated`, or `corrupted`. |
| `--verbose`, `-v` | Log debug detail. Applies to every command. `onfly serve` writes it to `server.log_file`; the others to standard error. |
| `--json-logs` | Write log records as one JSON object per line instead of text. `onfly index` then reports its steps as records rather than a progress bar. |
| `--config path` | Use this config file instead of walking parent directories. |
| `--version` | Print the build version. |

`onfly ext` reads `https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json`, or the URL in `ONFLY_REGISTRY_URL`. The registry manifest is limited to 1 MiB, and a warning is printed when `ONFLY_REGISTRY_URL` overrides the default. A downloaded file is kept only when its SHA-256 matches the registry. A mismatch deletes the temp file and leaves the project unchanged. Installed plugins are recorded in `.onfly/plugins/installed.json`. `onfly ext list` waits at most three seconds for the registry, then prints the table. A failed fetch adds a `registry:` line.

