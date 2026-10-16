# Onfly

> **Experimental, test repository.** Measured with Claude Code on five repositories (three runs per task; details in [docs/evaluation.md](docs/evaluation.md)):
>
> - Best case, on two medium repositories with questions about behavior: about 10% fewer turns and less time, 12 to 16% fewer output tokens, 6% less context sent to the model.
> - On the other three there was no gain: the assistant rarely called Onfly and found answers with Grep or Read.
> - Cost savings were not measured reliably (prompt caching dominates) and are unproven.

**Give your AI coding assistant a searchable map of your codebase.**

Onfly reads your project, splits it into meaningful pieces (functions, types, doc sections), and builds a local search index. Your IDE's assistant (Claude Code, Cursor, VS Code, anything that speaks [MCP](https://modelcontextprotocol.io)) then asks Onfly instead of guessing or reading whole files.

```text
You:        "Where do we validate the webhook signature?"
Assistant:  asks Onfly → internal/billing/webhook.go:42-71  VerifySignature
            reads just that function, answers with the right file and line.
```

## Why use it

- **Finds code by meaning**, not only by exact words. "retry with backoff" finds the retry helper even if it never says "backoff".
- **Small answers.** The assistant gets the 10 right chunks, not 40 files (see Status for what that saved in practice).
- **Knows your conventions.** Short rule files you write are attached to results.
- **Stays on your machine** if you use a local embedder such as Ollama.
- **Understands relationships**: who calls this function, what does it call, what breaks if I change it.

## Five-minute start

You need Go 1.27+ to build Onfly and an embedder. The easiest is [Ollama](https://ollama.com) (free, local).

```sh
# 1. Build Onfly (once)
make build                              # produces bin/onfly; put it on your PATH

# 2. Get an embedding model (once)
ollama pull qwen3-embedding:0.6b

# 3. In your project
cd ~/code/my-project
onfly init                              # creates .onfly/config.yaml
onfly ext install go                    # a Go parser; skip if you only want docs
onfly index                             # builds the index
onfly search "where is the config loaded"
```

Not a Go project? `onfly init` already enables the built-in text parser, so Markdown and text files are searchable right away. For other languages see [Parsers](docs/parsers.md).

Connect your IDE:

```sh
onfly mcp-config claude-code > .mcp.json      # or: cursor, vscode
onfly guide --write                           # tell the assistant when to use which tool (CLAUDE.md)
```

Restart the IDE, and ask a question about your code. That's it.

New here? [Getting started](docs/getting-started.md) walks through each step and what you should see. Something odd? [FAQ and troubleshooting](docs/faq.md).

## What your assistant can do with it

| Tool | Use it to |
| --- | --- |
| `search` | Find code by what it does. |
| `find` | Exact lookup: a string or identifier, or definitions by name (`Save*`). |
| `get_file_outline` | See what a file defines, without reading it. |
| `get_component_ast` | Fetch one function or type with its full code. |
| `graph` | Follow calls: who uses it, what it uses, what breaks. |
| `get_tests`, `get_history` | Which tests mention a symbol, and which commits touched it. |
| `read_file_raw`, `list_directory` | Opt-in (`server.file_tools: true`): read a file or browse a folder. |

Two ready-made prompts, `explain-component` and `review-impact`, chain these for you. Details are in [MCP server](docs/mcp.md).

## Everyday commands

```sh
onfly index                  # update the index after changes
onfly serve --watch          # serve your IDE and keep the index fresh
onfly search "question"      # try a search from the terminal
onfly doctor                 # something wrong? start here
onfly stats                  # how big is the index, what's in it, which chunk types
onfly eval tune              # find ranking weights that fit your code
```

Every command is listed in [Commands](docs/commands.md).

Results too noisy for your language, for example property or `use` chunks outranking classes? Drop or demote chunk types with `indexer.skip_types` and `search.type_weights`; see the [cookbook](docs/cookbook.md#quiet-small-declarations-properties-enum-cases-imports).

## How it works, in one picture

```text
 your files ──► parser ──► chunks ──► embedder ──► index (SQLite or Postgres)
 (.go, .md …)  (per language)  (functions,    (turns text       │
                                sections)      into vectors)    ▼
                                              IDE ◄── MCP ◄── onfly serve
```

Parsers are small plugins, one per language. Onfly ships a text parser and installs others with `onfly ext install`. Nothing is uploaded unless you choose a hosted embedder (OpenAI, Voyage) or reranker (Voyage, Cohere, Jina; Ollama keeps it local).

## Guides

Start here:

- [Getting started](docs/getting-started.md): the first hour, step by step
- [Cookbook](docs/cookbook.md): copy-paste recipes (monorepos, Postgres, CI, tuning)
- [FAQ and troubleshooting](docs/faq.md)

Reference:

- [Configuration](docs/configuration.md): every config key
- [Commands](docs/commands.md)
- [Rules](docs/rules.md): conventions attached to results
- [Parsers](docs/parsers.md): install, scope, or write one
- [Indexing](docs/indexing.md): what gets indexed and when
- [MCP server](docs/mcp.md): tools, arguments, results
- [Evaluation](docs/evaluation.md): measure whether search got better
- [Development](docs/development.md): build and test Onfly itself
