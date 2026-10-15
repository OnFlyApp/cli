# Getting started

This page takes you from nothing to an assistant that can search your code. It takes about ten minutes, most of it the first index.

## 1. Install the pieces

You need three things.

| Piece | What it does | How to get it |
| --- | --- | --- |
| **Onfly** | Builds the index and serves it to your IDE. | `make build`, then put `bin/onfly` on your `PATH`. |
| **An embedder** | Turns text into numbers so search can match meaning. | [Ollama](https://ollama.com) is free and local. OpenAI and Voyage also work (see the [Cookbook](cookbook.md)). |
| **A parser** (optional) | Splits a language's files into functions and types. | `onfly ext install go`, or none at all to start with docs. |

With Ollama:

```sh
ollama pull qwen3-embedding:0.6b
```

## 2. Create the config

In the root of your project:

```sh
onfly init
```

It prints what it created and the next steps:

```text
created .onfly/config.yaml
created .onfly/rules/project.md
created .onfly/plugins/
```

The `.onfly/` folder holds:

```text
.onfly/
  config.yaml       what to index, which embedder, where to store it
  rules/project.md  a note attached to search results (edit it)
  plugins/          parsers you install end up here
```

Open `.onfly/config.yaml`. It is commented, and the defaults work with Ollama. The parts you will most often change:

```yaml
embedder:
  provider: ollama                 # or openai, voyage
  model: qwen3-embedding:0.6b
  dimensions: 1024                 # must match the model

indexer:
  include: ["**/*"]                # which files may be indexed
  exclude: [vendor, node_modules]  # folders to always skip
  parsers:
    - extensions: [.md, .txt]      # which parser handles which files
      type: text
```

A file is indexed only when **all three** are true: it matches `include`, it is not excluded or git-ignored, and a parser handles its extension. If a file you expect is missing, one of those is the reason.

## 3. Add a parser for your language

The text parser handles `.md` and `.txt` out of the box. For code, install a language parser:

```sh
onfly ext list              # what is available
onfly ext install go        # example
```

This downloads the parser, checks its checksum, and adds it to your config for you.

Using another language? A parser is a small program. See [Parsers](parsers.md) for how to install a local one or write your own. Until then the text parser still makes your docs searchable.

## 4. Build the index

```sh
onfly index
```

You will see a progress line, then a summary:

```text
indexed 214 files, 1380 chunks, reused 0, skipped 0, pruned 0, failed 0
```

Run it again after editing code. Unchanged files are skipped, so it is fast. To preview what would be indexed without calling the embedder:

```sh
onfly index --dry-run
```

## 5. Try a search

```sh
onfly search "where do we read the config file"
```

```text
internal/config/config.go:120-168  Load
```

Each line is a file, a line range, and the symbol. If this finds the right place, the hard part is done.

## 6. Connect your IDE

```sh
onfly mcp-config claude-code > .mcp.json
```

Run without the redirect to see the entry, and which file it belongs in. Use `cursor` or `vscode` instead of `claude-code` for those editors; the output goes in `.cursor/mcp.json` and `.vscode/mcp.json`. Restart the IDE. Ask it something about your code, such as "how is authentication handled?", and watch it call the `search` tool.

Run `onfly guide --write` once to add a short "when to use which tool" block to `CLAUDE.md`. The server also sends it as instructions; the file copy helps clients that ignore them. Running it again replaces the block and leaves the rest of the file alone.

Every onfly tool is read-only. To stop Claude Code asking before each call, run `onfly mcp-config claude-code --allow` and merge the rule it prints into `.claude/settings.json`.

To keep the index fresh while you work, start the server with `--watch` (add it to `args` in the MCP entry), or set `server.watch: true` in the config.

## 7. Check your setup

```sh
onfly doctor
```

```text
config: ok
database: ok
embedder: ok
plugins: ok text
rules: ok 55 tokens
```

Every line should say `ok`. A line that does not tells you what to fix. Before the first `onfly index` it reports the database as missing, which is expected.

## Where to go next

- Tell the assistant your conventions: [Rules](rules.md).
- Fine-tune what gets indexed: [Cookbook](cookbook.md).
- Something did not work: [FAQ and troubleshooting](faq.md).
