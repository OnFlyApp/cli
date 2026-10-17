# MCP server

`onfly serve` is an MCP server on stdio. It identifies itself as `onfly` with the build version (`onfly --version`). Stdout is JSON-RPC only. Application logs, including the standard library logger, go to `server.log_file`.

The process loads the config, opens the database, and checks `embedder.dimensions` against the stored size. A mismatch stops the server before it accepts a session.

Logging never touches stdout, which carries only JSON-RPC: records go to `server.log_file`, at `server.log_level` (`debug`, `info`, `warn`, `error`), or at debug with `--verbose`, and as JSON with `--json-logs`. Even a logger pointed at stdout is redirected to stderr.

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

Global rules are in the server instructions, so the model sees them before it calls a tool. `onfly_xray` and `onfly_focus` append the rules that match the files they return inside the result, in `<architectural_rules>`, and send each rule at most once per MCP session: the server keeps a set of the rules it has already sent, in memory only, so restarting the server (a new session) sends them again. The atomic tools (`server.atomic_tools`) put matching rules in a `rules` field on every result, without that deduplication. A glob or a language name is included when the request path or a returned chunk path matches. A chunk type such as `interface` is included when a returned chunk has that type. The rules text is capped at `rules_engine.max_token_budget_percent` (default 15) of the token budget. Higher `priority` is kept. A dropped rule adds:

```text
// [Truncated: N rules omitted due to context limits.]
```

`onfly_xray` and `onfly_focus` (and, with `server.atomic_tools`, `search`, `find` and `graph`) then fit their code results into the remaining tokens. Frontmatter, templates, and fallback files are described in [configuration.md](configuration.md).

`onfly serve --max-tokens N` sets the search budget when `N` is greater than 0. Otherwise the budget is `server.max_tokens`, default 8192. The budget applies to the same tools. Rank order is kept. A chunk that does not fit beside the diagnostic line is dropped. If nothing fits, the only result is:

```text
// [Truncated: N matches omitted due to context limits.]
```

When `server.trace_file` is set, each tool call appends one JSON object to it (off by default): `ts`, `tool`, `args`, `duration_ms`, `tokens`, `chunks`. The line stores arguments, not file bodies.

When `server.usage_file` is set, the server also appends one line (`ts`, `query`, `file`, `symbol`, `rank`) each time `get_component_ast`, `graph`, `get_file_outline` or `read_file_raw` opens a chunk that the latest `search` returned (these are the atomic tools, so nothing is recorded with the default surface), up to ten minutes after it. It is off by default and the file contains your questions; see [configuration.md](configuration.md#usage-list).

The server instructions carry a short routing guide (`onfly_xray` before reading a large file, `onfly_focus` for a flow, and its `called_by` before a change). `onfly setup claude` registers the server in `.mcp.json` and writes that guide into `CLAUDE.md` in one step. `onfly mcp-config claude-code --allow` prints the `.claude/settings.json` rule (`mcp__onfly`) that lets the read-only tools run without a prompt. `onfly guide` prints the same text and `onfly guide --write` puts it in `CLAUDE.md` between `<!-- onfly:start -->` and `<!-- onfly:end -->` markers, for clients that ignore server instructions.

The server publishes resource `onfly://index`. A subscribed client is notified when the index generation changes. The server checks once a second. The first sample does not send a notification.

Every result is one compact JSON text block: no output schema and no `structuredContent`, so the two default tools cost about 300 tokens per session, and all twelve (`server.atomic_tools` and `server.file_tools` on) about 1,500 (2,475 before the schemas were dropped and the tools merged). When rules apply, their text comes first and the JSON follows on the last line. Every tool is annotated read-only.

A hit has `stale: true` when its file changed on disk after it was indexed (the content hash differs, or the file is gone); its line numbers may be wrong, so read the file before relying on them. Up to 20 files per result are checked.

Tool failures are tool results with `isError` set. They are not protocol errors. The model can read the message and try again.

## Tools

By default the server registers exactly two tools, `onfly_xray` and `onfly_focus`. The earlier tools (`search`, `find`, `graph`, `get_component_ast`, `get_file_outline`, `get_project_rules`, `get_tests`, `get_history`) are **legacy**: they are registered only when `server.atomic_tools: true`, because they overlap with an assistant's own `Read` and `Grep` and cost context on every session. `read_file_raw` and `list_directory` need `server.file_tools: true`. Sections marked *atomic* below describe those opt-in tools.

### `onfly_xray`

Inspects a file, a directory or a symbol with far fewer tokens than reading it. `target` is a project-relative file, a directory, or a symbol. A symbol may be qualified to pick among definitions: `telemetry.Open` or `Summary.Saved` (directory, file name, receiver type or namespace, then name), `Container::make`, or `internal/telemetry/telemetry.go:Open`. One match resolves to its file with that symbol expanded and its own `<definitions>` block. A name with several definitions (`Open`) returns up to five `<definition at="file:line">` blocks, code before tests, each with its signature, `calls` and `called_by`, and says how many more there are; a qualifier that matches nothing returns all of them with a `<note>`. `focus_symbol` keeps one method fully expanded. `depth` (1 to 4, default 1) is how many caller levels `called_by` follows.

`onfly_xray` does not rebuild code. The skeletonizer copies lines of the file on disk and replaces only the middle of long bodies, using the line ranges stored with each function and method chunk. Everything up to the line that opens the body is kept as written: the docblock, PHP 8 `#[...]` attributes, Go doc comments, a signature spread over several lines, a brace on its own line. The scanner reads the signature as code, so a `{` inside `(...)`, `[...]`, a string, a comment, `interface{}` or `struct{...}` does not end it. A body of 10 lines or fewer, the `focus_symbol`, and a span whose body brace cannot be found are left whole. The closing line is kept. Struct tags and type definitions are never touched.

Each line of the skeleton starts with its line number in the file, so a position read from the skeleton is a position in the file; a marker carries the number of the first line it hides, says which lines those are, and names the `focus_symbol` that expands the function. When anything is collapsed, the first line of the skeleton is a tip saying so. The numbers add a few tokens per line; they are what stops an assistant from counting lines in the collapsed text.

For a file the result is one text block:

```xml
<onfly_context target="app/Services/PaymentService.php" tokens_saved="3420">
<skeleton>the file, every line numbered as in the file, with every function or method body longer than 10 lines replaced by
12| // ... [body omitted: original lines 12-58, ~47 lines, ~410 tokens; focus_symbol="Name" expands it] ...</skeleton>
<blast_radius>
<calls>- signature [file:line]  (what the file uses, defined in other files)</calls>
<called_by>- signature [file:line]  (who calls the file's symbols, up to depth)</called_by>
</blast_radius>
<architectural_rules>project rules that match the path or chunk types</architectural_rules>
</onfly_context>
```

`calls` and `called_by` list one-line signatures. The index stores a call as a bare name, so each candidate is checked against the calling chunk's own text and its file's imports: `strings.Split` is not the project's `Split`, a bare call stays in its package, an imported package function must come from that import's directory, a call through a value must be a method, and a PHP `Class::method` must resolve (through `use` and the namespace) to that class. The check covers Go and PHP; other languages match by name. Calls through a value cannot be typed, so methods that many types implement (Go `String`, `Error`, `Read`, `Write`, `Close`, `Len`, `Less`, `Swap`, the JSON and text marshalers, `Scan`, `Value`; PHP `__construct`, `__toString`, `__invoke`, `toArray`, `jsonSerialize`, `handle` and the like) are attributed only to a definition in the caller's own file. Other value calls still match any method of that name. For an interface, a trait or an abstract class the answer also carries `<implementations of="Name">`: the types that declare `implements`, `extends` or (Rust) `impl Trait for` it, taken from what the parsers record, and, for a Go interface, the types that have a method of every name in it, marked `likely: has all its methods` because there is no type information. Embedded interfaces are not followed, and an interface of one method with more than ten matches lists none. The list stops at fifteen.

A directory lists each indexed file with its symbol names. The skeleton is cut to `server.max_tokens`. A target that is not a file, directory or symbol of the project is a tool error. `calls` and `called_by` never list documentation files, and a location points at the declaration line, past the docblock, comment or attribute that the parser keeps in the chunk.

If the index is empty, `onfly_xray` and `onfly_focus` first build it in process without the embedder (a second for a project of a hundred files, 13 s for two thousand). If the target file changed on disk since it was indexed, or was never indexed, that one file is synced before the answer. The embedding then continues in the background. The blast radius costs a fixed number of batched queries (one for the definitions of everything the file references, two per caller level), not one per symbol.

Each rule is sent once per MCP session, however many files match it, so the second call about the same directory carries no rules.

### `onfly_focus`

Searches by meaning and by name and returns a bundle. Ranking weights the lexical lists (keyword, per-word, symbol and path) 0.65 and the semantic ones 0.35, unlike `search`, moves declarations of four lines or fewer (small structs, types, constants) behind everything else unless the query names them, and lifts the symbols of a file or directory the query names: a term equal to a path segment (`skeleton` in `internal/skeleton/skeleton.go`) moves that file's chunks up, by as much as the word is rare, and adds up to six of its named symbols that no list returned, for at most three such files. Four more rules apply to `onfly_focus` only:

- **Rare words count more.** Each query word is weighted by `ln(1 + (N - df + 0.5) / (df + 0.5))` over the chunks that hold it, scaled to 0 to 1. The weight scales the per-word keyword list, the name, path and comment match, and the path boost. A word below 0.4 (about 5% of the chunks) names no module: `content` in a site generator no longer pulls in `create/content.go`. Counts stop at 5% of the chunks, so a very common word costs nothing to weigh.
- **Code first.** Markdown, text and reStructuredText files are left out of the candidate queries, not just ranked low, unless the query contains `docs`, `documentation`, `readme`, `guide`, `tutorial`, `changelog` or `markdown`. Test files, one-line `use` and `namespace` chunks, and small declarations rank last.
- **Whole-file coverage.** The files of the top 40 ranked chunks (at most 20) are read, and a chunk scores `1/rank + 1.5 × the share of the query's weight its whole file mentions`. A file whose functions together cover "table of contents headings" beats a chunk that matches one word. A word found nowhere in the index is left out of that share.
- **No embedder, no error.** When the embedder is not running, the semantic list is skipped for a minute and the answer is keyword, name and path only. The bundle is: `<symbols>` with the full code of the top `limit` matches (default 5, at most 20), `<related>` with one-line signatures of what they use, and the matching rules once per session.

### Telemetry

Both tools append one row per call to `telemetry.db` beside `server.log_file`: tool, target, the size of the files the answer replaced, the size of the answer, and the number of rules sent. `tokens_saved` in the result is the difference at four bytes per token, against a deliberately modest baseline: for `onfly_xray` the target file plus 150 tokens for each file it calls or is called from; for `onfly_focus` the whole files of the best three matches. `onfly stats` totals them: requests, tokens saved and the share they represent, dollars at $3 per million input tokens, and rules enforced. The file holds sizes and paths only.

### `onfly_env` (optional)

`server.tools: [onfly_env]`. Inventory of environment variables: the keys the `.env`, `.env.example`, `.env.schema` and similar files in the project root define (names only; a value is never read into the answer), where the code reads each one (`os.Getenv`, `env()`, `process.env`, `os.environ`, `env::var`, `ENV[...]`), the variables read but defined in no env file, and the ones defined but never read. A read with a default (`env('X', 'v')`, `os.getenv('X', 'v')`) is optional and is not reported as missing. Test files are not counted. `filter` keeps the names that contain a text. The same report is `onfly env`. Names built at run time (`os.Getenv(prefix + "X")`, or a constant) are not seen.

### `onfly_run` (optional)

`server.tools: [onfly_run]`. Runs a test or build command and returns only the failures. It starts a process, so it is not read-only: it is announced as such, and **nothing runs unless the command is listed in `run.commands`** in the config (word for word: `go test` allows `go test ./...` but not `go build`). There is no shell, so `;`, `|`, `&&`, redirections and `$(...)` are refused. Output is capped at 8 MiB and the run at `run.timeout_seconds` (default 120, at most 600).

`go test` is run with `-json`, so a failure is the failed test's own output with the file and line it names; a compile error is the build's output. Other runners (PHPUnit, pytest, Jest, Cargo) are read as text: control codes, progress dots and vendor and `node_modules` frames are dropped, the first failure block and the summary line are kept. For each failure the answer carries the code of the project function that contains the first lines it points at (at most two, 60 lines each): the test, and the code under test. A passing run is its summary line.

```xml
<onfly_run command="go test ./..." exit="1" duration="1.4s" tokens_saved="2290">
<summary>1 failed, 2 passed in 2 packages</summary>
<failures>
<failure name="TestAdd" package="calc">
<message>
calc_test.go:7: Add(2, 3) = -1, want 5
</message>
<code at="calc/calc_test.go:5">func TestAdd(t *testing.T) { ... }</code>
</failure>
</failures>
</onfly_run>
```

### `search` (atomic)

Finds code by what it does. Embeds `query` once and fuses semantic, keyword, per-word keyword and symbol-name lists, plus the lists of any second embedder, summary vector or summary keyword index that the project enables. Each side reads a candidate pool of `limit` raised to at least 20 and capped at 50. Rank in a list starts at 1. A repeated chunk keeps its first rank. The score is the sum of `weight / (rrf_k + rank)` over the lists; weights and `rrf_k` are in [configuration.md](configuration.md#search-fusion). Hits are sorted by score, then path, then text. A hit carries `summary` when the chunk has a generated one. The structured result is `{ "rules", "hits", "callers", "tests" }`. `hits` has `limit` entries. When the first hit is a named code chunk shown with its code, `callers` lists up to 5 chunks that reference it (one level, signatures only) and `tests` up to 5 test-file lines that mention its name, so a question about what uses or covers it needs no second call; both are omitted when empty. `rules` is omitted when nothing matches. An empty or whitespace `query` is a tool error and does not embed. A keyword error leaves the semantic hits in place.

| Argument | Required | Notes |
| --- | --- | --- |
| `query` | yes | Sent to the configured embedder and to keyword search. |
| `limit` | no | Default 10. Values below 1 become 10. Values above 50 become 50. |
| `workspace` | no | Empty uses the config value. `*` searches every workspace. Any other value matches that workspace. |
| `type` | no | Keep one chunk type, case-insensitive: `function`, `method`, `struct`, `section` (Markdown), and so on. Use `section` to search docs only, or any code type to leave docs out. |
| `path` | no | Keep files matching a glob relative to the project root, such as `internal/**` or `docs/**`. An invalid glob is a tool error. |
| `detail` | no | `auto` (default) keeps the code of the top 3 hits and reduces the rest to their first line (the signature) with `brief: true`; `full` keeps every body; `brief` reduces all. Fetch a brief hit's body with `get_component_ast`. Any other value is a tool error. |
| `changed_since` | no | A git ref. Keeps only files that differ from it in the working tree, untracked files included. `type`, `path` and `changed_since` filter the top 50 candidates of each list, so a narrow filter can return fewer than `limit` hits. A ref starting with `-` is rejected. |

If the embedder fails, the result is keyword hits only and a warning goes to the server log. Fusion weights come from `search` in [configuration.md](configuration.md#search-fusion).

If `workspace` is omitted, `query` is a single identifier, and that exact metadata name is stored in two or more workspaces, the tool returns an elicitation instead of searching. For the query `User` the message is `Which workspace should be searched for "User"?`. Decline does not embed. `*` and an explicit workspace do not ask.

### `get_component_ast` (atomic)

Returns the chunk whose `metadata.name` equals `name`, as a JSON object with an optional `rules` field. Split parts of that symbol in the same file are joined. An unknown name is a tool error. When several definitions share the name (for example `Save` on two types), the earliest chunk is returned and the others are listed under `alternatives` as `{file_path, receiver, start_line}`, capped at 20 with the dropped count in `alternatives_omitted`. Pass `receiver` to pick one; an unknown receiver is a tool error.

| Argument | Required |
| --- | --- |
| `name` | yes |
| `receiver` | no |

### `get_tests` (atomic)

Finds the tests that mention a symbol. Tests are usually not indexed, so this scans the project's test files directly: a file counts as a test file by name (`*_test.go`, `test_*.py`, `*_test.py`, `*.test.*` and `*.spec.*` for JS and TS, `*Test.php`, `*Test.java`, `*Test.kt`, `*Tests.cs`, `*Tests.swift`, `*_spec.rb`, `*_test.rb`, `*_test.rs`) or by living under a `tests`, `test`, `__tests__` or `spec` directory. Files ignored by `.gitignore` or `indexer.exclude` are skipped. Rust unit tests inside `src/` files are not found.

Returns `{ "tests", "truncated" }`. Each test is `{file_path, line, enclosing, snippet}`: the matching line, and the nearest test declaration above it when one is recognised (`func Test`, `def test_`, `it(`, `describe(`, `#[test]`, `[Fact]`, `@Test` and similar). The match is a whole word, not a call, so a common name such as `Run` also finds unrelated text. At most 20 hits are returned and `truncated` is true when more existed or 2000 test files were scanned.

| Argument | Required | Notes |
| --- | --- | --- |
| `name` | yes | |
| `path` | no | Only scan test files matching this glob. |

### `get_history` (atomic)

Returns the commits that touched a symbol's lines, newest first: `{ "file_path", "start_line", "end_line", "scope", "commits" }`, with `hash`, `author`, `date` and `subject` per commit. Uses `git log -L`, so it follows the lines, not the file. A definition without a line range falls back to the file's log and `scope` is `file`. Outside a git repository, or for a file that is not committed, it is a tool error.

| Argument | Required | Notes |
| --- | --- | --- |
| `name` | yes | |
| `receiver` | no | Picks one of several same-name definitions. |
| `file_path` | no | Same. |
| `limit` | no | 1 to 20, default 5. |

### `find` (atomic)

Exact lookup, no embedding. `kind: "text"` finds chunks whose text contains the string or identifier, case-insensitively (substring search plus the full-text index, 20 rows, `{ "hits" }` like `search`). `kind: "symbol"` lists definitions by name, from the index: a pattern with `*` or `?` is a wildcard match over the whole name, anything else is a prefix; the result is `{ "symbols", "omitted" }`. `kind` defaults to `symbol` when `query` contains `*` or `?`, otherwise `text`. For a description of behavior, call `search`.

| Argument | Required | Notes |
| --- | --- | --- |
| `query` | yes | |
| `kind` | no | `text` or `symbol`. |
| `limit` | no | Symbol results, 1 to 50, default 20. |
| `workspace` | no | Text kind only. Same meaning as in `search`. |

### `graph` (atomic)

Follows calls from a symbol. Returns `{ "hits" }`.

- `direction: "callers"` (default): the distinct chunks that call `name`, directly or through callers, nearest first, at most 100. `depth` is the number of caller levels, 1 to 4, default 2. Without `receiver` or `file_path` the walk follows names, so two functions with the same name share callers. With either, the start is one definition, and a caller nearer to another same-name definition (same file, then same directory, then shared parent) is dropped. Use this before changing a function.
- `direction: "callees"`: the chunks for each name in the component's `outbound` list that is defined in this index, at most 50. Library and builtin names are skipped. A callee defined in several places resolves to the definition nearest the caller. An unknown component is a tool error.
- `direction: "references"`: the chunks whose `outbound` list contains `name`, at most 100. An unknown name returns no hits. Indexing fills `outbound` from `Name(` and `.Name` in the chunk text.

| Argument | Required | Notes |
| --- | --- | --- |
| `name` | yes | |
| `direction` | no | `callers`, `callees` or `references`. |
| `depth` | no | Callers only. |
| `detail` | no | `brief` (default): each hit's first line (the signature) and `brief: true`; `auto`: code for the top 3; `full`: every body. Use `get_component_ast` for one body. |
| `receiver` | no | Picks one of several same-name definitions. |
| `file_path` | no | Same. |

### `get_file_outline` (atomic)

Returns `{ "rules", "symbols", "omitted" }` for one indexed file, in file order. Each symbol has `name`, `type`, `receiver`, `part`, `start_line`, and `end_line`; unnamed chunks have no `name`. At most 300 symbols; `omitted` counts the rest. It reads the index only, so it costs far fewer tokens than `read_file_raw` and shows what a file defines. A path that is not indexed, or that leaves the project, is a tool error.

| Argument | Required |
| --- | --- |
| `file_path` | yes |

### `read_file_raw` (file tools)

Off by default; set `server.file_tools: true` to register it and `list_directory`. They duplicate an assistant's own file tools and cost context on every session.

Reads a file inside the project root. The path may be relative or absolute. `..` and a symlink that resolves outside the root are rejected. Secret names (`.env*`, `id_rsa*`, `id_ed25519`, `*.pem`, `*.key`, `*.p12`, `.npmrc`, `.netrc`, `.aws`, `.ssh`, `credentials.json`) are refused with case ignored, so `.ENV` is refused. Paths matched by a `.gitignore` or named in `indexer.exclude` are refused too, and `list_directory` omits them. `.gitignore` files are read when the server starts. Files larger than 1 MiB are rejected.

| Argument | Required |
| --- | --- |
| `path` | yes |

The result is `{ "rules", "path", "content" }`. `rules` is omitted when nothing matches.

### `list_directory` (file tools)

Lists one directory level. An empty `path` lists the project root. Names are sorted. `kind` is `file` or `dir`. Symlinks are reported as files. The result is `{ "rules", "entries" }`.

| Argument | Required | Notes |
| --- | --- | --- |
| `path` | no | Relative to the project root, or absolute inside it. |

### `get_project_rules` (atomic)

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
