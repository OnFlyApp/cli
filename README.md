# Onfly — Context Compressor & Architectural Interceptor for AI Coding Agents

> **Experimental, test repository.** Retrieval scores on Go and PHP repositories of 40 to 5,000 files are in [Evaluation](docs/evaluation.md#large-repositories-and-other-languages). Savings in agent runs are modest and depend on how often the assistant calls Onfly instead of `Grep` and `Read`; cost savings are not measured.

Stop burning thousands of tokens on whole-file `Read`s and noisy `Grep`s. Onfly is a CGO-free Go CLI and MCP server that parses your code into language-aware chunks with WebAssembly parsers, then gives Claude Code, Cursor or any MCP client two tools: **`onfly_xray`** (a file's skeleton, who calls it, what it calls, your project rules) and **`onfly_focus`** (the best-matching code plus what it uses), each in one round-trip. Each call comes back smaller than reading the files it replaces; the estimate is in `onfly stats`.

## Why not just `Read` and `Grep`?

| | Native agent flow | With Onfly |
| --- | --- | --- |
| Understand a file | `Read` the whole file, every method body | `onfly_xray`: signatures, attributes and docblocks kept, long bodies collapsed to `// ... [body omitted: original lines 12-58, ~47 lines, ~410 tokens; focus_symbol="Name" expands it] ...`, every line numbered as in the file |
| Who calls this? | Several `Grep`s, then `Read` each hit | `called_by` and `calls` in the same answer, resolved through imports so `strings.Split` is not your `Split` |
| Find a flow | `Grep` for guessed words, read the false hits | `onfly_focus`: ranked by name and meaning, full code of the best few plus signatures of what they use |
| Project conventions | Whatever the model remembered from `CLAUDE.md` | The rules that match the returned paths ride along in `<architectural_rules>`, once per session |
| Cost visibility | None | `onfly stats` totals the estimated tokens saved |

## 30-second quickstart

You need Go 1.27+ to build Onfly. An embedder is optional: without one, names, text and paths are searched and everything else works; [Ollama](https://ollama.com) (free, local) adds search by meaning.

```sh
git clone https://github.com/OnFlyApp/cli onfly && cd onfly && make build   # bin/onfly; put it on your PATH
ollama pull qwen3-embedding:0.6b                                            # the default embedder, once

cd ~/code/my-project
onfly init            # .onfly/config.yaml, .onfly/ in .gitignore, parsers for the languages it finds, first index
onfly setup claude    # register the MCP server in .mcp.json and write the guide into CLAUDE.md
```

Restart Claude Code and ask about your code. `init` indexes without the embedder, so it finishes in seconds (2,000 Go files in 13 s). The MCP tools also build or refresh the index on first use if it is missing or a file changed on disk, and add the vectors for semantic search in the background when the embedder is up. `onfly index` embeds by hand; `onfly index --lexical` skips the embedder on purpose. Later, `onfly stats` prints the index breakdown and, once the tools have run, requests served, estimated tokens and dollars saved, and rules injected (`onfly stats --format json` prints just those totals for CI). Cursor and VS Code: `onfly mcp-config cursor` or `onfly mcp-config vscode`. If `init` could not install a parser (no network), it prints the `onfly ext install <language>` line to run. Details: [Getting started](docs/getting-started.md).

## What the tools return

`onfly_xray(target, focus_symbol?, depth?)` where `target` is a file, a directory or a symbol. A symbol can be qualified (`telemetry.Open`, `Summary.Saved`, `Container::make`, `internal/telemetry/telemetry.go:Open`); a bare name that several files define (`Open`) returns every definition with its own callers, so no second call is needed. A trimmed real answer for `internal/mcp/xray.go` in this repository:

```xml
<onfly_context target="internal/mcp/xray.go" tokens_saved="1865">
<skeleton>
// [Tip: a "body omitted" line is a collapsed function; the numbers on the left are line numbers of the original file. To read one body call onfly_xray with focus_symbol="<name>" (or read those lines).]
36| func (h *tools) xray(ctx context.Context, req *sdkmcp.CallToolRequest, in xrayIn) (*sdkmcp.CallToolResult, struct{}, error) {
37| 	start := time.Now()
38| 	res, out, err := h.runXray(ctx, req, in)
39| 	h.record("onfly_xray", in, start, resultText(res), 0)
40| 	return res, out, err
41| }
42|
43| func (h *tools) runXray(ctx context.Context, _ *sdkmcp.CallToolRequest, in xrayIn) (*sdkmcp.CallToolResult, struct{}, error) {
44| 	// ... [body omitted: original lines 44-74, ~31 lines, ~299 tokens; focus_symbol="runXray" expands it] ...
75| }
</skeleton>
<blast_radius>
<calls>
- func Collapse(src string, spans []Span, focus string, minLines int) string { [internal/skeleton/skeleton.go:16]
- func (h *tools) rulesOnce(q rules.Query) (string, int) { [internal/mcp/macro.go:19]
</calls>
<called_by>
- func NewServer(cfg config.Config, store vectorstore.Store, ...) (*sdkmcp.Server, func(), error) { [internal/mcp/server.go:143]
</called_by>
</blast_radius>
<architectural_rules>
# Project
- Build with `CGO_ENABLED=0`. Do not add a dependency that needs cgo.
</architectural_rules>
</onfly_context>
```

`onfly_focus(query, limit?)` returns `<symbols>` (full code of the top matches), `<related>` (one-line signatures of what they use) and the rules. Ranking weights the name, path and word lists 0.65 and the embedder 0.35. Query words count by rarity (a word in 5% of the chunks adds little), documentation files stay out of the candidates unless the query asks for docs, test files and one-line imports rank last, and files that cover more of the question as a whole rank higher. Parameters and edge cases: [MCP server](docs/mcp.md).

Under the hood:

- **Parsers are WebAssembly plugins** ([wazero](https://wazero.io), no CGO, no host access). The official Go parser uses `go/parser`; the PHP, TypeScript, JavaScript, Python and Rust parsers use a pure-Go Tree-sitter port. They install with `onfly ext install go|php|typescript|javascript|python|rust`. Any program that prints chunks can be a parser.
- **The skeletonizer copies lines, it never re-renders code.** Docblocks, PHP 8 `#[...]` attributes, Go struct tags and multi-line signatures survive byte for byte; only the middle of long bodies is replaced, and the scanner ignores braces inside parameters, strings and `interface{}`.
- **Calls are resolved from the caller's imports and namespaces** (Go and PHP), so a standard-library call is not attributed to a same-named function in your repository, and a PHP `$this->name()` resolves only to the caller's own class, a class it imports, or its namespace. Blast-radius queries are batched, a fixed number per call.
- **Generated code is left out.** Files that say "Code generated ... DO NOT EDIT", `zz_generated*` and `*.pb.go` are not indexed (17% of the chunks in Kubernetes); `indexer.index_generated: true` keeps them.
- **Locations point at the declaration**, not at the docblock above it.
- **Rules are delivered, not requested.** Matching rules from `.onfly/rules/` (or `CLAUDE.md` and friends) are appended to the tool result once per MCP session, held in memory.
- **Telemetry is conservative.** The baseline for `onfly_xray` is the file plus 150 tokens for each file it calls or is called from; for `onfly_focus`, the whole files of the three best matches. Tokens are bytes divided by four.

## Optional: summaries from a local model

Chunk summaries make search by meaning better and cost index time. Point Onfly at any local server with three variables, no config needed:

```sh
export ONFLY_SLM_PROVIDER=ollama          # or openai, for mlx_lm.server, llama-server, LM Studio, vLLM
export ONFLY_SLM_URL=http://localhost:11434
export ONFLY_SLM_MODEL=qwen2.5-coder:7b   # then: onfly index
```

A summary is made once per changed chunk and cached; a server that is down only skips them. See [Configuration](docs/configuration.md#chunk-summaries).

## The tool surface

| Tool | Registered | Use it to |
| --- | --- | --- |
| `onfly_xray` | always | Inspect a file, folder or symbol. |
| `onfly_focus` | always | Find code by meaning and name. |
| `get_tests`, `get_history`, `search`, `find`, `graph`, `get_component_ast`, `get_file_outline`, `get_project_rules` | `server.atomic_tools: true` (legacy) | Tests that mention a symbol, recent commits, and the single-purpose tools from earlier versions. |
| `read_file_raw`, `list_directory` | `server.file_tools: true` | Read a file or browse a folder. |
| `onfly_env` | `server.tools: [onfly_env]` | The environment variables a project defines (names only) and where the code reads them. |
| `onfly_run` | `server.tools: [onfly_run]` and `run.commands` | Run an allowed test or build command and get back only the failures, with the code they point at. Starts processes, so not read-only. |

The two default tools cost about 300 tokens of tool definitions per session.

## Everyday commands

```sh
onfly index                  # update the index after changes
onfly serve --watch          # serve your IDE and keep the index fresh
onfly search "question"      # try a search from the terminal
onfly doctor                 # something wrong? start here
onfly stats                  # index breakdown and token savings
onfly lint                   # check a change against forbidden_calls / forbidden_imports in the rules
onfly audit                  # where an assistant's tokens go, from telemetry and its session logs
onfly env                    # environment variables defined and read
onfly eval tune              # find ranking weights that fit your code
```

Every command is in [Commands](docs/commands.md). Results too noisy for your language? See `indexer.skip_types` and `search.type_weights` in the [cookbook](docs/cookbook.md#quiet-small-declarations-properties-enum-cases-imports).

## How it works

```text
 your files ──► parser ──► chunks ──► index (SQLite or Postgres) ◄── embedder (optional,
 (.go, .php …) (WASM, per    (functions,       │                        text to vectors,
                language)     sections)        ▼                        added in the background)
                                  assistant ◄── MCP ◄── onfly serve
```

Chunks are searchable by name, text and path as soon as they are parsed; the embedder adds search by meaning when it is running, and a missing or failing embedder never stops indexing. Nothing is uploaded unless you choose a hosted embedder (OpenAI, Voyage) or reranker (Voyage, Cohere, Jina). Beyond the two tools, `onfly lint`, `onfly audit` and `onfly env` use the same index; see [Commands](docs/commands.md) and the [v3 roadmap](docs/ROADMAP-V3.md).

## Guides

Start here: [Getting started](docs/getting-started.md), [Cookbook](docs/cookbook.md), [FAQ](docs/faq.md).

Reference: [Configuration](docs/configuration.md), [Commands](docs/commands.md), [Rules](docs/rules.md), [Parsers](docs/parsers.md), [Indexing](docs/indexing.md), [MCP server](docs/mcp.md), [Evaluation](docs/evaluation.md), [Development](docs/development.md).

## License

Onfly is released under the [Functional Source License, Version 1.1, ALv2 Future License](LICENSE) (FSL-1.1-ALv2), the license used across the OnFlyApp organization. You may use, copy, modify and redistribute it for any purpose except a competing commercial use, and each version converts to the Apache License 2.0 two years after it is released. The parsers in [OnFlyApp/ext-parser-*](https://github.com/OnFlyApp) and the [registry](https://github.com/OnFlyApp/registry) carry the same license.
