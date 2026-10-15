# MCP server

`onfly serve` is an MCP server on stdio. It identifies itself as `onfly` with the build version (`onfly --version`). Stdout is JSON-RPC only. Application logs, including the standard library logger, go to `server.log_file`.

The process loads the config, opens the database, and checks `embedder.dimensions` against the stored size. A mismatch stops the server before it accepts a session.

Start it from the project root so config discovery works:

```sh
onfly serve
```

Or pin the file:

```sh
onfly serve --config /abs/path/to/project/.onfly/config.yaml
```

Claude Code and Cursor accept the same server entry. Set `cwd` to the project if you do not pass `--config`.

```json
{
  "mcpServers": {
    "onfly": {
      "command": "onfly",
      "args": ["serve", "--config", "/abs/path/to/project/.onfly/config.yaml"]
    }
  }
}
```

`command` must be on `PATH`, or use the absolute path of `bin/onfly`.

Global rules are in the server instructions, so the model sees them before it calls a tool. Matching rules are also text on the tool result and a structured `rules` field. A glob or a language name is included when the request path or a returned chunk path matches. A chunk type such as `interface` is included when a returned chunk has that type. The rules text is capped at `rules_engine.max_token_budget_percent` (default 15) of the token budget. Higher `priority` is kept. A dropped rule adds:

```text
// [Truncated: N rules omitted due to context limits.]
```

`search`, `find` (text kind) and `graph` then fit their code results into the remaining tokens. Frontmatter, templates, and fallback files are described in [configuration.md](configuration.md).

`onfly serve --max-tokens N` sets the search budget when `N` is greater than 0. Otherwise the budget is `server.max_tokens`, default 8192. The budget applies to `search`, `find` (text kind) and `graph`. Rank order is kept. A chunk that does not fit beside the diagnostic line is dropped. If nothing fits, the only result is:

```text
// [Truncated: N matches omitted due to context limits.]
```

When `server.trace_file` is set, each tool call appends one JSON object to it (off by default): `ts`, `tool`, `args`, `duration_ms`, `tokens`, `chunks`. The line stores arguments, not file bodies.

When `server.usage_file` is set, the server also appends one line (`ts`, `query`, `file`, `symbol`, `rank`) each time `get_component_ast`, `graph`, `get_file_outline` or `read_file_raw` opens a chunk that the latest `search` returned, up to ten minutes after it. It is off by default and the file contains your questions; see [configuration.md](configuration.md#usage-list).

The server instructions carry a short routing guide (`search` for behavior, `get_component_ast` for a known symbol, `graph` and `get_tests` before a change, `get_history` for risk). `onfly mcp-config claude-code --allow` prints the `.claude/settings.json` rule (`mcp__onfly`) that lets the read-only tools run without a prompt. `onfly guide` prints the same text and `onfly guide --write` puts it in `CLAUDE.md` between `<!-- onfly:start -->` and `<!-- onfly:end -->` markers, for clients that ignore server instructions.

The server publishes resource `onfly://index`. A subscribed client is notified when the index generation changes. The server checks once a second. The first sample does not send a notification.

Every result is one compact JSON text block: no output schema and no `structuredContent`, so tool definitions cost about 1,000 tokens per session (2,475 before the schemas were dropped and the tools merged). When rules apply, their text comes first and the JSON follows on the last line. Every tool is annotated read-only.

A hit has `stale: true` when its file changed on disk after it was indexed (the content hash differs, or the file is gone); its line numbers may be wrong, so read the file before relying on them. Up to 20 files per result are checked.

Tool failures are tool results with `isError` set. They are not protocol errors. The model can read the message and try again.

## Tools

### `search`

Finds code by what it does. Embeds `query` once and fuses semantic, keyword, per-word keyword and symbol-name lists, plus the lists of any second embedder, summary vector or summary keyword index that the project enables. Each side reads a candidate pool of `limit` raised to at least 20 and capped at 50. Rank in a list starts at 1. A repeated chunk keeps its first rank. The score is the sum of `weight / (rrf_k + rank)` over the lists; weights and `rrf_k` are in [configuration.md](configuration.md#search-fusion). Hits are sorted by score, then path, then text. A hit carries `summary` when the chunk has a generated one. The structured result is `{ "rules", "hits" }`. `hits` has `limit` entries. `rules` is omitted when nothing matches. An empty or whitespace `query` is a tool error and does not embed. A keyword error leaves the semantic hits in place.

| Argument | Required | Notes |
| --- | --- | --- |
| `query` | yes | Sent to the configured embedder and to keyword search. |
| `limit` | no | Default 10. Values below 1 become 10. Values above 50 become 50. |
| `workspace` | no | Empty uses the config value. `*` searches every workspace. Any other value matches that workspace. |
| `type` | no | Keep one chunk type, case-insensitive: `function`, `method`, `struct`, `section` (Markdown), and so on. Use `section` to search docs only, or any code type to leave docs out. |
| `path` | no | Keep files matching a glob relative to the project root, such as `internal/**` or `docs/**`. An invalid glob is a tool error. |
| `detail` | no | `auto` (default) keeps the code of the top 3 hits and reduces the rest to their first line (the signature) with `brief: true`; `full` keeps every body; `brief` reduces all. Fetch a brief hit's body with `get_component_ast`. Any other value is a tool error. |
| `changed_since` | no | A git ref. Keeps only files that differ from it in the working tree, untracked files included. `type`, `path` and `changed_since` filter the top 50 candidates of each list, so a narrow filter can return fewer than `limit` hits. A ref starting with `-` is rejected. |

If the embedder fails, the result is keyword hits only and a warning goes to the server log. Fusion weights and the recency list come from `search` in [configuration.md](configuration.md#search-fusion).

If `workspace` is omitted, `query` is a single identifier, and that exact metadata name is stored in two or more workspaces, the tool returns an elicitation instead of searching. For the query `User` the message is `Which workspace should be searched for "User"?`. Decline does not embed. `*` and an explicit workspace do not ask.

### `get_component_ast`

Returns the chunk whose `metadata.name` equals `name`, as a JSON object with an optional `rules` field. Split parts of that symbol in the same file are joined. An unknown name is a tool error. When several definitions share the name (for example `Save` on two types), the earliest chunk is returned and the others are listed under `alternatives` as `{file_path, receiver, start_line}`, capped at 20 with the dropped count in `alternatives_omitted`. Pass `receiver` to pick one; an unknown receiver is a tool error.

| Argument | Required |
| --- | --- |
| `name` | yes |
| `receiver` | no |

### `get_tests`

Finds the tests that mention a symbol. Tests are usually not indexed, so this scans the project's test files directly: a file counts as a test file by name (`*_test.go`, `test_*.py`, `*_test.py`, `*.test.*` and `*.spec.*` for JS and TS, `*Test.php`, `*Test.java`, `*Test.kt`, `*Tests.cs`, `*Tests.swift`, `*_spec.rb`, `*_test.rb`, `*_test.rs`) or by living under a `tests`, `test`, `__tests__` or `spec` directory. Files ignored by `.gitignore` or `indexer.exclude` are skipped. Rust unit tests inside `src/` files are not found.

Returns `{ "tests", "truncated" }`. Each test is `{file_path, line, enclosing, snippet}`: the matching line, and the nearest test declaration above it when one is recognised (`func Test`, `def test_`, `it(`, `describe(`, `#[test]`, `[Fact]`, `@Test` and similar). The match is a whole word, not a call, so a common name such as `Run` also finds unrelated text. At most 20 hits are returned and `truncated` is true when more existed or 2000 test files were scanned.

| Argument | Required | Notes |
| --- | --- | --- |
| `name` | yes | |
| `path` | no | Only scan test files matching this glob. |

### `get_history`

Returns the commits that touched a symbol's lines, newest first: `{ "file_path", "start_line", "end_line", "scope", "commits" }`, with `hash`, `author`, `date` and `subject` per commit. Uses `git log -L`, so it follows the lines, not the file. A definition without a line range falls back to the file's log and `scope` is `file`. Outside a git repository, or for a file that is not committed, it is a tool error.

| Argument | Required | Notes |
| --- | --- | --- |
| `name` | yes | |
| `receiver` | no | Picks one of several same-name definitions. |
| `file_path` | no | Same. |
| `limit` | no | 1 to 20, default 5. |

### `find`

Exact lookup, no embedding. `kind: "text"` finds chunks whose text contains the string or identifier, case-insensitively (substring search plus the full-text index, 20 rows, `{ "hits" }` like `search`). `kind: "symbol"` lists definitions by name, from the index: a pattern with `*` or `?` is a wildcard match over the whole name, anything else is a prefix; the result is `{ "symbols", "omitted" }`. `kind` defaults to `symbol` when `query` contains `*` or `?`, otherwise `text`. For a description of behavior, call `search`.

| Argument | Required | Notes |
| --- | --- | --- |
| `query` | yes | |
| `kind` | no | `text` or `symbol`. |
| `limit` | no | Symbol results, 1 to 50, default 20. |
| `workspace` | no | Text kind only. Same meaning as in `search`. |

### `graph`

Follows calls from a symbol. Returns `{ "hits" }`.

- `direction: "callers"` (default): the distinct chunks that call `name`, directly or through callers, nearest first, at most 100. `depth` is the number of caller levels, 1 to 4, default 2. Without `receiver` or `file_path` the walk follows names, so two functions with the same name share callers. With either, the start is one definition, and a caller nearer to another same-name definition (same file, then same directory, then shared parent) is dropped. Use this before changing a function.
- `direction: "callees"`: the chunks for each name in the component's `outbound` list that is defined in this index, at most 50. Library and builtin names are skipped. A callee defined in several places resolves to the definition nearest the caller. An unknown component is a tool error.
- `direction: "references"`: the chunks whose `outbound` list contains `name`, at most 100. An unknown name returns no hits. Indexing fills `outbound` from `Name(` and `.Name` in the chunk text.

| Argument | Required | Notes |
| --- | --- | --- |
| `name` | yes | |
| `direction` | no | `callers`, `callees` or `references`. |
| `depth` | no | Callers only. |
| `receiver` | no | Picks one of several same-name definitions. |
| `file_path` | no | Same. |

### `get_file_outline`

Returns `{ "rules", "symbols", "omitted" }` for one indexed file, in file order. Each symbol has `name`, `type`, `receiver`, `part`, `start_line`, and `end_line`; unnamed chunks have no `name`. At most 300 symbols; `omitted` counts the rest. It reads the index only, so it costs far fewer tokens than `read_file_raw` and shows what a file defines. A path that is not indexed, or that leaves the project, is a tool error.

| Argument | Required |
| --- | --- |
| `file_path` | yes |

### `read_file_raw`

Off by default; set `server.file_tools: true` to register it and `list_directory`. They duplicate an assistant's own file tools and cost context on every session.

Reads a file inside the project root. The path may be relative or absolute. `..` and a symlink that resolves outside the root are rejected. Secret names (`.env*`, `id_rsa*`, `id_ed25519`, `*.pem`, `*.key`, `*.p12`, `.npmrc`, `.netrc`, `.aws`, `.ssh`, `credentials.json`) are refused with case ignored, so `.ENV` is refused. Paths matched by a `.gitignore` or named in `indexer.exclude` are refused too, and `list_directory` omits them. `.gitignore` files are read when the server starts. Files larger than 1 MiB are rejected.

| Argument | Required |
| --- | --- |
| `path` | yes |

The result is `{ "rules", "path", "content" }`. `rules` is omitted when nothing matches.

### `list_directory`

Lists one directory level. An empty `path` lists the project root. Names are sorted. `kind` is `file` or `dir`. Symlinks are reported as files. The result is `{ "rules", "entries" }`.

| Argument | Required | Notes |
| --- | --- | --- |
| `path` | no | Relative to the project root, or absolute inside it. |

### `get_project_rules`

No arguments. Returns the rendered rules that apply to every request, highest priority first, inside the rules token budget. A missing file from `rules` is a tool error. Path and syntax rules are not included here; they are on the tool call that requested that file or chunk, and global rules are in the server instructions.

## Hit object

`search`, `find` (text kind) and `graph` put this object in `hits`:

```json
{
  "file_path": "internal/mcp/server.go",
  "symbol": "search",
  "receiver": "tools",
  "start_line": 160,
  "end_line": 228,
  "lines": "func (h *tools) search..."
}
```

`symbol` is `metadata.name`, or an empty string. `receiver` is present only when `metadata.receiver` is set. `lines` is the chunk text. `start_line` and `end_line` are 1-based and inclusive. The indexer fills them when the parser left them empty, from the first match of the chunk text in the file. If that text is not in the file, both fields are omitted.

`get_component_ast` returns this chunk object, plus `rules` when a rule matches:

```json
{
  "file_path": "src/svc.go",
  "type": "method",
  "content": "func (s *Service) Execute() {}",
  "metadata": { "name": "Execute", "receiver": "Service" },
  "outbound": ["User"]
}
```

## Watching while serving

`onfly serve --watch`, or `server.watch: true`, reindexes changed files while the server runs. It loads the parsers and the embedder in the serve process. Watcher errors go to the server log, never stdout, and do not stop the server.

## Prompts

The server offers two prompts that chain the tools above. Clients that support MCP prompts show them as commands.

| Prompt | Argument | What it asks the model to do |
| --- | --- | --- |
| `explain-component` | `name` | Read the component with `get_component_ast`, its dependencies with `graph` (callees), its users with `graph` (references), then explain it with file and line citations. |
| `review-impact` | `name` | Read the component, walk callers with `graph` at depth 3, group them by file, and say which tests to run. |

A missing `name` is an error.
