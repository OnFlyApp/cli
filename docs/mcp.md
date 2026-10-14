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

`search`, `search_keyword`, `get_references`, `get_callees`, and `get_impact` then fit their code results into the remaining tokens. Frontmatter, templates, and fallback files are described in [configuration.md](configuration.md).

`onfly serve --max-tokens N` sets the search budget when `N` is greater than 0. Otherwise the budget is `server.max_tokens`, default 8192. The budget applies to `search`, `search_keyword`, `get_references`, `get_callees`, and `get_impact`. Rank order is kept. A chunk that does not fit beside the diagnostic line is dropped. If nothing fits, the only result is:

```text
// [Truncated: N matches omitted due to context limits.]
```

When `server.trace_file` is set, each tool call appends one JSON object to it (off by default): `ts`, `tool`, `args`, `duration_ms`, `tokens`, `chunks`. The line stores arguments, not file bodies.

The server publishes resource `onfly://index`. A subscribed client is notified when the index generation changes. The server checks once a second. The first sample does not send a notification.

Tool failures are tool results with `isError` set. They are not protocol errors. The model can read the message and try again.

## Tools

### `search`

Finds code by what it does. Embeds `query` once, runs semantic search and keyword search, and fuses the two lists. Each side reads a candidate pool of `limit` raised to at least 20 and capped at 50. Rank in a list starts at 1. A repeated chunk keeps its first rank. The score is the sum of `1 / (60 + rank)`. Hits are sorted by score, then path, then text. The structured result is `{ "rules", "hits" }`. `hits` has `limit` entries. `rules` is omitted when nothing matches. An empty or whitespace `query` is a tool error and does not embed. A keyword error leaves the semantic hits in place.

| Argument | Required | Notes |
| --- | --- | --- |
| `query` | yes | Sent to the configured embedder and to keyword search. |
| `limit` | no | Default 10. Values below 1 become 10. Values above 50 become 50. |
| `workspace` | no | Empty uses the config value. `*` searches every workspace. Any other value matches that workspace. |

If `workspace` is omitted, `query` is a single identifier, and that exact metadata name is stored in two or more workspaces, the tool returns an elicitation instead of searching. For the query `User` the message is `Which workspace should be searched for "User"?`. Decline does not embed. `*` and an explicit workspace do not ask.

### `search_keyword`

Finds chunks whose text contains an identifier the caller already knows. For a description of behavior, call `search`. Case-insensitive substring search, plus the full-text index. There is no limit argument. The server asks the store for 20 rows. It does not embed and does not fuse. The structured result is `{ "rules", "hits" }`, the same hit object as `search`.

| Argument | Required | Notes |
| --- | --- | --- |
| `keyword` | yes | |
| `workspace` | no | Empty uses the config value. `*` searches every workspace. Any other value matches that workspace. |

### `get_component_ast`

Returns the chunk whose `metadata.name` equals `name`, as a JSON object with an optional `rules` field. Split parts of that symbol in the same file are joined. An unknown name is a tool error. When several definitions share the name (for example `Save` on two types), the earliest chunk is returned and the others are listed under `alternatives` as `{file_path, receiver, start_line}`. Pass `receiver` to pick one; an unknown receiver is a tool error.

| Argument | Required |
| --- | --- |
| `name` | yes |
| `receiver` | no |

### `get_references`

Returns `{ "rules", "chunks" }`. `chunks` are the rows whose `outbound` list contains `name`, at most 100. An unknown name returns an empty `chunks` array. Indexing fills `outbound` from `Name(` and `.Name` in the chunk text.

| Argument | Required |
| --- | --- |
| `name` | yes |

### `get_callees`

Returns `{ "rules", "hits" }`. `hits` are the chunks for each name in the component's `outbound` list that is defined in this index, at most 50. Library and builtin names are skipped. An unknown component is a tool error.

| Argument | Required |
| --- | --- |
| `name` | yes |

### `get_impact`

Returns `{ "rules", "hits" }`. `hits` are the distinct chunks that call `name`, directly or through callers, nearest first, at most 100. `depth` is the number of caller levels, from 1 to 4, default 2. The walk follows names, so two functions with the same name share callers.

| Argument | Required |
| --- | --- |
| `name` | yes |
| `depth` | no |

### `read_file_raw`

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

`search` and `search_keyword` put this object in `hits`:

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

`get_component_ast` returns this chunk object, plus `rules` when a rule matches. `get_references` puts the same object in `chunks`:

```json
{
  "file_path": "src/svc.go",
  "type": "method",
  "content": "func (s *Service) Execute() {}",
  "metadata": { "name": "Execute", "receiver": "Service" },
  "outbound": ["User"]
}
```
