# Onfly

**Give your AI coding assistant a searchable map of your codebase.**

Onfly reads your project, splits it into meaningful pieces (functions, types, doc sections), and builds a local search index. Your IDE's assistant (Claude Code, Cursor, VS Code, anything that speaks [MCP](https://modelcontextprotocol.io)) then asks Onfly instead of guessing or reading whole files.

```text
You:        "Where do we validate the webhook signature?"
Assistant:  asks Onfly → internal/billing/webhook.go:42-71  VerifySignature
            reads just that function, answers with the right file and line.
```

## Why use it

- **Finds code by meaning**, not only by exact words. "retry with backoff" finds the retry helper even if it never says "backoff".
- **Cheap on tokens.** The assistant gets the 10 right chunks, not 40 files.
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
```

Restart the IDE, and ask a question about your code. That's it.

New here? [Getting started](docs/getting-started.md) walks through each step and what you should see. Something odd? [FAQ and troubleshooting](docs/faq.md).

## What your assistant can do with it

| Tool | Use it to |
| --- | --- |
| `search` | Find code by what it does. |
| `search_keyword` | Find an exact identifier or string. |
| `find_symbols` | List definitions by name: `Save*`, `parse`. |
| `get_file_outline` | See what a file defines, without reading it. |
| `get_component_ast` | Fetch one function or type with its full code. |
| `get_references`, `get_callees`, `get_impact` | Follow calls: who uses it, what it uses, what breaks. |
| `read_file_raw`, `list_directory` | Read a file or browse a folder. |

Two ready-made prompts, `explain-component` and `review-impact`, chain these for you. Details are in [MCP server](docs/mcp.md).

## Everyday commands

```sh
onfly index                  # update the index after changes
onfly serve --watch          # serve your IDE and keep the index fresh
onfly search "question"      # try a search from the terminal
onfly doctor                 # something wrong? start here
onfly stats                  # how big is the index, what's in it
onfly eval tune              # find ranking weights that fit your code
```

Every command is listed in [Commands](docs/commands.md).

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
