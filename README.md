# Onfly — Context Compressor & Architectural Interceptor for AI Coding Agents

> **Experimental, test repository.** Measured with Claude Code on five repositories (three runs per task; details in [docs/evaluation.md](docs/evaluation.md)):
>
> - Best case, on two medium repositories with questions about behavior: about 10% fewer turns and less time, 12 to 16% fewer output tokens, 6% less context sent to the model.
> - On the other three there was no gain: the assistant rarely called Onfly and found answers with Grep or Read.
> - Cost savings were not measured reliably (prompt caching dominates) and are unproven.

Stop burning thousands of tokens on whole-file `Read`s and noisy `Grep`s. Onfly is a CGO-free Go CLI and MCP server that parses your code into language-aware chunks with WebAssembly parsers, then gives Claude Code, Cursor or any MCP client two tools: **`onfly_xray`** (a file's skeleton, who calls it, what it calls, your project rules) and **`onfly_focus`** (the best-matching code plus what it uses), each in one round-trip. In our self-test each call came back about half the size (an estimate) of reading the files it replaces; the measured end-to-end effect on agent runs is smaller, see the note above.

## Why not just `Read` and `Grep`?

| | Native agent flow | With Onfly |
| --- | --- | --- |
| Understand a file | `Read` the whole file, every method body | `onfly_xray`: signatures, attributes and docblocks kept, long bodies collapsed to `// ... [body omitted: lines 12-58, ~410 tokens] ...` |
| Who calls this? | Several `Grep`s, then `Read` each hit | `called_by` and `calls` in the same answer, resolved through imports so `strings.Split` is not your `Split` |
| Find a flow | `Grep` for guessed words, read the false hits | `onfly_focus`: ranked by name and meaning, full code of the best few plus signatures of what they use |
| Project conventions | Whatever the model remembered from `CLAUDE.md` | The rules that match the returned paths ride along in `<architectural_rules>`, once per session |
| Cost visibility | None | `onfly stats` totals the estimated tokens saved |

## 30-second quickstart

You need Go 1.27+ to build Onfly and an embedder; the easiest is [Ollama](https://ollama.com) (free, local).

```sh
git clone https://github.com/OnFlyApp/cli onfly && cd onfly && make build   # bin/onfly; put it on your PATH
ollama pull qwen3-embedding:0.6b                                            # the default embedder, once

cd ~/code/my-project
onfly init            # .onfly/config.yaml, .onfly/ in .gitignore, parsers for the languages it finds, first index
onfly setup claude    # register the MCP server in .mcp.json and write the guide into CLAUDE.md
```

Restart Claude Code and ask about your code. The MCP tools build or refresh the index on first use if it is missing or stale, in about a second and without the embedder: names, text and paths are searchable at once, and the vectors for semantic search are added in the background when the embedder is up (`onfly index` runs it by hand; `onfly index --lexical` skips the embedder). Later, `onfly stats` prints the index breakdown and, once the tools have run, requests served, estimated tokens and dollars saved, and rules injected (`onfly stats --format json` prints just those totals for CI). Cursor and VS Code: `onfly mcp-config cursor` or `onfly mcp-config vscode`. If `init` could not install a parser (no network), it prints the `onfly ext install <language>` line to run. Details: [Getting started](docs/getting-started.md).

## What the tools return

`onfly_xray(target, focus_symbol?, depth?)` where `target` is a file, a directory or a symbol name. A trimmed real answer for `internal/mcp/xray.go` in this repository:

```xml
<onfly_context target="internal/mcp/xray.go" tokens_saved="1865">
<skeleton>
func (h *tools) xray(ctx context.Context, req *sdkmcp.CallToolRequest, in xrayIn) (*sdkmcp.CallToolResult, struct{}, error) {
	start := time.Now()
	res, out, err := h.runXray(ctx, req, in)
	h.record("onfly_xray", in, start, resultText(res), 0)
	return res, out, err
}

func (h *tools) runXray(ctx context.Context, _ *sdkmcp.CallToolRequest, in xrayIn) (*sdkmcp.CallToolResult, struct{}, error) {
	// ... [body omitted: lines 44-74, ~299 tokens] ...
}
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

`onfly_focus(query, limit?)` returns `<symbols>` (full code of the top matches), `<related>` (one-line signatures of what they use) and the rules. Ranking weights the name, path and word lists 0.65 and the embedder 0.35, and lifts the symbols of a file or directory your query names. Parameters and edge cases: [MCP server](docs/mcp.md).

Under the hood:

- **Parsers are WebAssembly plugins** ([wazero](https://wazero.io), no CGO, no host access). The official Go parser uses `go/parser`; the PHP, TypeScript, JavaScript, Python and Rust parsers use a pure-Go Tree-sitter port. They install with `onfly ext install go|php|typescript|javascript|python|rust`. Any program that prints chunks can be a parser.
- **The skeletonizer copies lines, it never re-renders code.** Docblocks, PHP 8 `#[...]` attributes, Go struct tags and multi-line signatures survive byte for byte; only the middle of long bodies is replaced, and the scanner ignores braces inside parameters, strings and `interface{}`.
- **Calls are resolved from the caller's imports and namespaces** (Go and PHP), so a standard-library call is not attributed to a same-named function in your repository. Blast-radius queries are batched, a fixed number per call.
- **Rules are delivered, not requested.** Matching rules from `.onfly/rules/` (or `CLAUDE.md` and friends) are appended to the tool result once per MCP session, held in memory.
- **Telemetry is conservative.** The baseline for `onfly_xray` is the file plus 150 tokens for each file it calls or is called from; for `onfly_focus`, the whole files of the three best matches. Tokens are bytes divided by four.

## Optional: summaries from a local model

Chunk summaries make search by meaning better (MRR 0.663 to 0.751 on this repository, see [Evaluation](docs/evaluation.md)) and cost index time. Point Onfly at any local server with three variables, no config needed:

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

The two default tools cost about 300 tokens of tool definitions per session.

## Everyday commands

```sh
onfly index                  # update the index after changes
onfly serve --watch          # serve your IDE and keep the index fresh
onfly search "question"      # try a search from the terminal
onfly doctor                 # something wrong? start here
onfly stats                  # index breakdown and token savings
onfly eval tune              # find ranking weights that fit your code
```

Every command is in [Commands](docs/commands.md). Results too noisy for your language? See `indexer.skip_types` and `search.type_weights` in the [cookbook](docs/cookbook.md#quiet-small-declarations-properties-enum-cases-imports).

## How it works

```text
 your files ──► parser ──► chunks ──► embedder ──► index (SQLite or Postgres)
 (.go, .php …) (WASM, per    (functions,   (text to          │
                language)     sections)     vectors)         ▼
                                           assistant ◄── MCP ◄── onfly serve
```

Nothing is uploaded unless you choose a hosted embedder (OpenAI, Voyage) or reranker (Voyage, Cohere, Jina).

## Guides

Start here: [Getting started](docs/getting-started.md), [Cookbook](docs/cookbook.md), [FAQ](docs/faq.md).

Reference: [Configuration](docs/configuration.md), [Commands](docs/commands.md), [Rules](docs/rules.md), [Parsers](docs/parsers.md), [Indexing](docs/indexing.md), [MCP server](docs/mcp.md), [Evaluation](docs/evaluation.md), [Development](docs/development.md).
