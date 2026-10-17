# Getting started

This page takes you from nothing to an assistant that can search your code. `onfly init` indexes your project in seconds without any other software; an embedder adds search by meaning and takes minutes to hours depending on the size of the project.

## 1. Install the pieces

You need three things.

| Piece | What it does | How to get it |
| --- | --- | --- |
| **Onfly** | Builds the index and serves it to your IDE. | `make build`, then put `bin/onfly` on your `PATH`. |
| **An embedder** (optional) | Turns text into numbers so search can match meaning. Without one, names, text and paths are still searched. | [Ollama](https://ollama.com) is free and local. OpenAI and Voyage also work (see the [Cookbook](cookbook.md)). |
| **A parser** | Splits a language's files into functions and types. | `onfly init` installs one for each language it finds from a manifest (`go.mod`, `composer.json`, `package.json`, ...); otherwise `onfly ext install go`. |

With Ollama:

```sh
ollama pull qwen3-embedding:0.6b
```

## 2. Create the config

In the root of your project:

```sh
onfly init
```

It creates the config, installs the parsers for the languages it finds, and builds the first index without the embedder. It prints what it did and the next steps:

```text
created .onfly/config.yaml
created .onfly/rules/project.md
created .onfly/plugins/
installed go 0.1.0
indexed 214 files, 1380 chunks (semantic vectors: run onfly index with your embedder up)
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

## 3. Add a parser for another language

`init` already installed a parser for each language it found from a manifest. The text parser handles `.md` and `.txt` out of the box. For another language, install a parser:

```sh
onfly ext list              # what is available
onfly ext install go        # example
```

This downloads the parser, checks its checksum, and adds it to your config for you.

Using another language? A parser is a small program. See [Parsers](parsers.md) for how to install a local one or write your own. Until then the text parser still makes your docs searchable.

## 4. Add search by meaning

`init` made names, text and paths searchable. With the embedder running, add the vectors for search by meaning:

```sh
onfly index
```

The MCP tools do this in the background too, so you can skip this step and keep working; run it yourself to see progress. You will see a progress line, then a summary:

```text
indexed 214 files, 1380 chunks, reused 0, skipped 0, pruned 0, failed 0
```

Run it again after editing code. Unchanged files are skipped, so it is fast. If the embedder is not running, indexing still succeeds and says how many files are stored without vectors; run it again once the embedder is up. To store names and text without the embedder on purpose, or to preview what would be indexed:

```sh
onfly index --lexical     # no embedder, every core
onfly index --dry-run     # parse and count, store nothing
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
onfly setup claude          # registers the server in .mcp.json and writes the guide into CLAUDE.md
# or by hand: onfly mcp-config claude-code > .mcp.json
```

Run without the redirect to see the entry, and which file it belongs in. Use `cursor` or `vscode` instead of `claude-code` for those editors; the output goes in `.cursor/mcp.json` and `.vscode/mcp.json`. Restart the IDE. Ask it something about your code, such as "how is authentication handled?", and watch it call `onfly_xray` or `onfly_focus`.

Run `onfly guide --write` once to add a short "when to use which tool" block to `CLAUDE.md`. The server also sends it as instructions; the file copy helps clients that ignore them. Running it again replaces the block and leaves the rest of the file alone.

The default tools are read-only (`onfly_run`, if you turn it on, is not). To stop Claude Code asking before each call, run `onfly mcp-config claude-code --allow` and merge the rule it prints into `.claude/settings.json`.

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

Every line should say `ok`. A line that does not tells you what to fix.

## Where to go next

- Check a change against your rules before you commit: `onfly lint` ([Rules](rules.md#constraints-checked-by-onfly-lint)).
- See where an assistant spends tokens: `onfly audit`. List the environment variables your code reads: `onfly env`.
- Turn on `onfly_env` or `onfly_run` for the assistant: [MCP server](mcp.md#onfly_env-optional).

- Tell the assistant your conventions: [Rules](rules.md).
- Fine-tune what gets indexed: [Cookbook](cookbook.md).
- Something did not work: [FAQ and troubleshooting](faq.md).
