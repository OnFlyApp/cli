# Onfly: a context compressor for AI coding agents

> Experimental. Retrieval and agent benchmarks on six code bases, with the numbers and their limits, are in [Evaluation](docs/evaluation.md#benchmarks) and summarized [below](#benchmarks). Savings in agent runs depend on how often the assistant calls Onfly instead of `Grep` and `Read`; cost savings are not measured.

Onfly is a single Go binary with no CGO. It parses your code into chunks with WebAssembly parsers ([wazero](https://wazero.io)), keeps them in SQLite (or Postgres), and gives Claude Code, Cursor or any MCP client two tools, each answering in one round-trip: `onfly_xray` returns a file's skeleton, who calls it, what it calls and your project rules, and `onfly_focus` returns the best-matching code and what it uses. Search is local-first and hybrid: names, text and paths, plus vectors from [Ollama](https://ollama.com) (`qwen3-embedding`) for questions that describe behavior in words the code does not use. A call comes back smaller than the files it replaces; `onfly stats` shows the estimate.

## Why not just `Read` and `Grep`?

| | Native agent flow | With Onfly |
| --- | --- | --- |
| Understand a file | `Read` the whole file, every method body | `onfly_xray`: signatures, attributes and docblocks kept, long bodies collapsed to `// ... [body omitted: original lines 12-58, ~47 lines, ~410 tokens; focus_symbol="Name" expands it] ...`, every line numbered as in the file |
| Who calls this? | Several `Grep`s, then `Read` each hit | `called_by` and `calls` in the same answer, resolved through imports, parameter and property types, so `strings.Split` is not your `Split`; the entrypoint two hops up is on the same line |
| Find a flow | `Grep` for guessed words, read the false hits | `onfly_focus`: ranked by name and meaning, full code of the best few plus signatures of what they use |
| Project conventions | Whatever the model remembered from `CLAUDE.md` | The rules that match the returned paths ride along, once per session |
| Cost visibility | None | `onfly stats` totals the estimated tokens saved |

## Quickstart

You need Go 1.27+ to build Onfly. An embedder is optional: without one, names, text and paths are searched and everything else works; [Ollama](https://ollama.com) (free, local) adds search by meaning.

```sh
git clone https://github.com/OnFlyApp/cli onfly && cd onfly && make build   # bin/onfly; put it on your PATH
ollama pull qwen3-embedding:0.6b                                            # the default embedder, once

cd ~/code/my-project
onfly init            # .onfly/config.yaml, .onfly/ in .gitignore, parsers for the languages it finds, first index
onfly setup claude    # register the MCP server in .mcp.json and write the guide into CLAUDE.md
```

Restart Claude Code and ask about your code.

`init` indexes without the embedder, so it finishes in seconds (2,000 Go files in 13 s). The MCP tools build or refresh the index on first use if it is missing or a file changed on disk, and add the vectors for search by meaning in the background when the embedder is up. `onfly index` embeds by hand; `onfly index --lexical` skips the embedder on purpose.

`onfly stats` prints the index breakdown and, once the tools have run, requests served, estimated tokens and dollars saved, and rules injected. `onfly stats --format json` prints just those totals for CI. For Cursor and VS Code use `onfly mcp-config cursor` or `onfly mcp-config vscode`. If `init` could not install a parser (no network), it prints the `onfly ext install <language>` command to run. More in [Getting started](docs/getting-started.md).

## What the tools return

`onfly_xray(target, focus_symbol?, depth?)` where `target` is a file, a directory or a symbol. A symbol can be qualified (`telemetry.Open`, `Summary.Saved`, `Container::make`, `internal/telemetry/telemetry.go:Open`); a bare name that several files define (`Open`) returns every definition with its own callers, so no second call is needed. A trimmed real answer, for a name that is only close (`expandFileSumm`) in this repository:

```text
# onfly target="internal/query/filesummary.go" tokens_saved=104
> no symbol "expandFileSumm" in this file; expanded the closest by name: expandFileSummaries
## expandFileSummaries
23| // expandFileSummaries replaces each file summary in the ranking, at its place,
...
29| func (s *Searcher) expandFileSummaries(ctx context.Context, query string, ranked []parser.Chunk) []parser.Chunk {
...
## scoreAgainst (called by the above, same file)
...
## skeleton
// [Tip: a "body omitted" line is a collapsed function; the numbers on the left are line numbers of the original file. ...]
 29| func (s *Searcher) expandFileSummaries(...) []parser.Chunk {
 30|     // ... [body omitted: lines 30-95, ~540 tokens; focus_symbol="expandFileSummaries"] ...
...
## calls
- func ChunkKey(c parser.Chunk) string { [internal/query/query.go:345]
## called_by
- func (s *Searcher) RunFocus(ctx context.Context, r Request) ([]parser.Chunk, error) { [internal/query/focus.go:38] <- tools::runFocus (focus.go)
## rules
- Build with `CGO_ENABLED=0`. Do not add a dependency that needs cgo.
```

What it does for the model's next step:

- **The body asked for comes first**, numbered as in the file, ahead of the skeleton, which a long file's token budget would cut. A name that is not in the file expands the **closest names** (`end` for `endCycle`) and says so.
- **Same-file helpers** that the symbol calls are expanded too, in the room the token budget leaves: the next call the model would make.
- **`called_by` follows the language.** A call is attributed through the caller's imports and namespace, the declared type of a parameter or property (`$this->limiter->tooManyAttempts()` is `Cache\RateLimiter`, not the neighbour in the same namespace), a variable named for its class, and a method name only one class defines. A caller with one to three callers of its own shows them on its line (**two hops**), so the entrypoint is visible without a second call.
- **Command and event handlers.** Parsers mark a method that handles a type (`__invoke(CancelOrder $command)`, `handle(SomeEvent $e)`); the code that builds that type is listed as a caller of the handler, since a bus, not a call, joins them.

`onfly_focus(query, limit?)` returns `## symbols` (full code of the top matches), `## related` (one-line signatures of what they use) and the rules. The vector list weighs four times the lexical ones, because a question describes behavior and shares few words with the code; every file also has a short **file summary** chunk (its path, namespace, types and the names it defines) that carries the file's vocabulary into the vectors, and a summary that ranks is replaced by the method of that file which answers the question best. Query words count by rarity, documentation files stay out of the candidates unless the query asks for docs, test files and one-line imports rank last, and an identifier in the question is also searched by its parts (`handleJobException`: handle, job, exception). The tool description asks the calling model to add likely code verbs to its query (for retry: `retry release attempts backoff`) and to switch to `onfly_xray` on a directory instead of repeating a miss. Parameters and edge cases: [MCP server](docs/mcp.md).

Under the hood:

- Parsers are WebAssembly plugins (wazero, no CGO, no host access). The official Go parser uses `go/parser`; the PHP, TypeScript, JavaScript, Python and Rust parsers use a pure-Go Tree-sitter port. They install with `onfly ext install go|php|typescript|javascript|python|rust`. Any program that prints chunks can be a parser, and `wasm_path:` points a project at your own `.wasm` without a release; the [contract](docs/parsers.md) covers metadata such as `param_types` and `handles`.
- The skeletonizer copies lines of the file and never re-renders code. Docblocks, PHP 8 `#[...]` attributes, Go struct tags and multi-line signatures survive byte for byte; only the middle of long bodies is replaced, and the scanner ignores braces inside parameters, strings and `interface{}`.
- Calls are resolved from the caller's imports and namespaces (Go and PHP), so a standard-library call is not attributed to a same-named function in your repository. Blast-radius queries are batched, a fixed number per call.
- Generated code and noise are left out by per-language rules you can change: files that say "Code generated ... DO NOT EDIT", `zz_generated*`, `*.pb.go`, `_ide_helper*.php`, and `declare(strict_types=1);` statements (17% of the chunks in Kubernetes are generated; `indexer.index_generated: true` keeps them).
- Embedding is resilient: a batch an Ollama server times out on is split in halves instead of retried whole, and a file indexed before file summaries existed gets its summary without any other vector being recomputed.
- Locations point at the declaration line, below any docblock.
- Matching rules from `.onfly/rules/` (or `CLAUDE.md` and friends) are appended to the tool result once per MCP session, held in memory, so the assistant does not have to ask for them.
- Telemetry uses a modest baseline: for `onfly_xray` the file plus 150 tokens for each file it calls or is called from, for `onfly_focus` the whole files of the three best matches. Tokens are bytes divided by four.

## Benchmarks

Five open-source code bases (Cobra, Hugo, this repository, Laravel, Kubernetes `pkg/`), `qwen3-embedding:0.6b` through Ollama. Every number below is one run; with 8 questions a difference of one or two answers is noise. Method, suites and caveats: [Evaluation](docs/evaluation.md#benchmarks).

**Retrieval**, `onfly_focus`, file-level recall@5 on questions that name no function:

| | Cobra | Hugo | Onfly | Laravel | Kubernetes |
| --- | --- | --- | --- | --- | --- |
| Lexical index (no embedder) | 0.75 | 0.43 | 0.30 | 0.12 | 0.12 |
| Hybrid index | **1.00** | **0.57** | **0.60** | **0.50** | **0.50** |

Naming a function is not the hard case: 30 sampled "where is `X` defined" questions per repository (Cobra, Hugo, Onfly, Laravel, Kubernetes) find it in the top five **150 of 150 times** (recall@5 1.00, MRR 0.96 to 0.98).

**Agents**, `Grep`/`Read` (A) against `onfly_xray`/`onfly_focus` (B), one run each, run one at a time, answers graded against the source (42 questions):

| | Cobra | Hugo | Onfly | Laravel | Kubernetes | Total |
| --- | --- | --- | --- | --- | --- | --- |
| A: correct / partial / wrong | 8/0/0 | 8/0/0 | 10/0/0 | 8/0/0 | 7/1/0 | **41/1/0** |
| B: correct / partial / wrong | 6/2/0 | 5/3/0 | 7/3/0 | 7/1/0 | 6/2/0 | **31/11/0** |
| Tokens A / B | 47.7k / 63.4k | 67.2k / 82.6k | 66.4k / 66.9k | 49.9k / 68.8k | 70.8k / 88.7k | 302k / 370k (+23%) |
| Seconds A / B | 37 / 62 | 110 / 95 | 85 / 53 | 41 / 62 | 86 / 71 | 358 / 343 (-4%) |
| Calls A / B (onfly + native) | 5 / 13+2 | 17 / 23+3 | 9 / 24+2 | 5 / 25+0 | 20 / 21+1 | 56 / 106+8 |

Honest reading: on questions that describe behavior in words, the agent with `Grep` was as complete and cheaper; the Onfly agent stopped earlier and hedged ("I did not see…") on 11 of 42 answers, made twice the calls because `onfly_focus` often needed a second query, and spent 23% more tokens. No answer was wrong in either arm. Onfly was faster on three repositories (the ones where `Grep` needed 17 to 20 calls) and about even in total time. An earlier run in which Onfly beat `Grep` on Laravel (8 of 8 against 4 of 8) did not repeat: `Grep` scored 8 of 8 in five calls this time. Per-kind breakdown and the reasons: [Evaluation](docs/evaluation.md#agents).

## Configuration

Everything below is optional, and everything has a default. `onfly init` writes a commented `.onfly/config.yaml`; `onfly doctor` validates it and names the sections that change a default.

```yaml
embedder:
  provider: ollama
  endpoint: http://localhost:11434
  model: qwen3-embedding:0.6b     # the Qwen3 query instruction is added for you
  dimensions: 1024
  timeout_seconds: 120            # a batch that times out is split in halves
  batch_size: 32

indexer:
  include: ["**/*"]
  exclude: [vendor, node_modules]
  parsers:
    - extensions: [.php]
      type: wasm
      path: .onfly/plugins/php-parser.wasm
      ignore_directives: [declare]                       # declare(strict_types=1) is not a chunk
      generated: ["_ide_helper*.php"]                    # machine-written files are left out
    - extensions: [.twig]
      wasm_path: ./plugins/twig.wasm                     # your own parser, no release needed
      exclude: ["**/vendor/**"]

search:                           # onfly_focus
  focus_semantic_weight: 2
  focus_keyword_weight: 0.5
  file_summary_weight: 0.7
  candidate_pool: 20

xray:                             # how much onfly_xray prints
  whole_file_lines: 120           # a file this short is shown whole
  preview_callers: 3              # callers shown two hops up
  max_helpers: 2                  # same-file helpers expanded with a symbol

resolver:
  max_unambiguous_definitions: 1  # a method only one class defines is attributed to an untyped call
```

Every key, its default and what it changes: [Configuration](docs/configuration.md).

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

Every command is in [Commands](docs/commands.md). If small declarations crowd the results in your language, see `indexer.skip_types` and `search.type_weights` in the [cookbook](docs/cookbook.md#quiet-small-declarations-properties-enum-cases-imports).

## How it works

```text
 your files ──► parser ──► chunks ──► index (SQLite or Postgres) ◄── embedder (optional,
 (.go, .php …) (WASM, per    (functions,       │                        text to vectors,
                language)     sections)        ▼                        added in the background)
                                  assistant ◄── MCP ◄── onfly serve
```

Chunks are searchable by name, text and path as soon as they are parsed; the embedder adds search by meaning when it is running, and a missing or failing embedder never stops indexing. Nothing is uploaded unless you choose a hosted embedder (OpenAI, Voyage) or reranker (Voyage, Cohere, Jina). Beyond the two tools, `onfly lint`, `onfly audit` and `onfly env` use the same index; see [Commands](docs/commands.md).

## Guides

Start here: [Getting started](docs/getting-started.md), [Cookbook](docs/cookbook.md), [FAQ](docs/faq.md).

Reference: [Configuration](docs/configuration.md), [Commands](docs/commands.md), [Rules](docs/rules.md), [Parsers](docs/parsers.md), [Indexing](docs/indexing.md), [MCP server](docs/mcp.md), [Evaluation](docs/evaluation.md), [Development](docs/development.md).

## License

Onfly is released under the [Functional Source License, Version 1.1, ALv2 Future License](LICENSE) (FSL-1.1-ALv2), the license used across the OnFlyApp organization. You may use, copy, modify and redistribute it for any purpose except a competing commercial use, and each version converts to the Apache License 2.0 two years after it is released. The parsers in [OnFlyApp/ext-parser-*](https://github.com/OnFlyApp) and the [registry](https://github.com/OnFlyApp/registry) carry the same license.
