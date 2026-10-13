# MCP server

`onfly serve` is an MCP server on stdio. It identifies itself as `onfly` `0.3.0`. Stdout is JSON-RPC only. Application logs, including the standard library logger, go to `server.log_file`.

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

`onfly serve --max-tokens N` sets the search budget when `N` is greater than 0. Otherwise the budget is `server.max_tokens`, default 8192. The budget applies to `search_semantic`, `search_keyword`, and `get_references`. Rank order is kept. A chunk that does not fit beside the diagnostic line is dropped. If nothing fits, the only result is:

```text
// [Truncated: N matches omitted due to context limits. Use search_keyword to refine.]
```

Each tool call appends one JSON object to `server.trace_file` (default `.onfly/mcp_trace.jsonl`): `ts`, `tool`, `args`, `duration_ms`, `tokens`, `chunks`. The line stores arguments, not file bodies.

The server publishes resource `onfly://index`. A subscribed client is notified when the index generation changes. The server checks once a second. The first sample does not send a notification.

Tool failures are tool results with `isError` set. They are not protocol errors. The model can read the message and try again.

## Tools

### `search_semantic`

Embeds `query` and returns the nearest chunks.

| Argument | Required | Notes |
| --- | --- | --- |
| `query` | yes | Sent to the configured embedder. |
| `limit` | no | Default 10. Values below 1 become 10. Values above 50 become 50. |
| `workspace` | no | Empty uses the config value. `*` searches every workspace. Any other value matches that workspace. |

If `workspace` is omitted, `query` is a single identifier, and that exact metadata name is stored in two or more workspaces, the tool returns an elicitation instead of searching. For the query `User` the message is `Which workspace should be searched for "User"?`. Decline does not embed. `*` and an explicit workspace do not ask.

### `search_keyword`

Case-insensitive substring search, plus the full-text index. There is no limit argument. The server asks the store for 20 rows.

| Argument | Required | Notes |
| --- | --- | --- |
| `keyword` | yes | |
| `workspace` | no | Same rules as `search_semantic`. |

### `get_component_ast`

Returns the chunk whose `metadata.name` equals `name`, as a JSON object. An unknown name is a tool error.

| Argument | Required |
| --- | --- |
| `name` | yes |

### `get_references`

Returns chunks whose `outbound` list contains `name`, at most 100. An unknown name returns an empty list.

| Argument | Required |
| --- | --- |
| `name` | yes |

### `read_file_raw`

Reads a file inside the project root. The path may be relative or absolute. `..` and a symlink that resolves outside the root are rejected. Secret names are refused with case ignored, so `.ENV` is refused. Files larger than 1 MiB are rejected.

| Argument | Required |
| --- | --- |
| `path` | yes |

The result is `{ "path", "content" }`.

### `list_directory`

Lists one directory level. An empty `path` lists the project root. Names are sorted. `kind` is `file` or `dir`. Symlinks are reported as files.

| Argument | Required | Notes |
| --- | --- | --- |
| `path` | no | Relative to the project root, or absolute inside it. |

### `get_project_rules`

No arguments. Reads every file in `rules` and concatenates them. A missing rule file is a tool error.

## Chunk object

Search and `get_component_ast` return:

```json
{
  "file_path": "src/svc.go",
  "type": "method",
  "content": "func (s *Service) Execute() {}",
  "metadata": { "name": "Execute", "receiver": "Service" },
  "outbound": ["User"]
}
```
