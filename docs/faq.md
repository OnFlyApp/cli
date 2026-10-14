# FAQ and troubleshooting

## The basics

### What is Onfly, in one sentence?

A local search index for your code that AI assistants can query over MCP, so they find the right code without reading everything.

### Does my code leave my machine?

Only to the embedder you choose. With **Ollama**, nothing leaves. With **OpenAI** or **Voyage**, the text of each chunk is sent to that provider to be embedded. The optional **reranker** (Voyage, Cohere or Jina) also sends the top search results, unless you point it at your own server. Files that look like secrets (`.env*`, `*.pem`, `*.key`, `id_rsa*`, `.npmrc` and similar) are never indexed or served.

### Can I rerank with Ollama?

Not directly. Ollama has embeddings and text generation but, as far as we know, no rerank endpoint. Reranking works with Voyage, Cohere, Jina, or any self-hosted server that speaks their request format (vLLM, llama.cpp `--reranking`, Infinity). See [Reranker](configuration.md#reranker).

### What does it cost?

Onfly is free. Ollama is free. Hosted embedders charge per token, and you pay mostly on the first index, because unchanged files are never embedded again.

### Which languages work?

The built-in text parser covers Markdown and plain text. Go has a published parser (`onfly ext install go`). Any other language needs a parser plugin; see [Parsers](parsers.md). You can write one in any language: it reads a file on stdin and prints JSON.

### Do I need Docker or a database server?

No. The default is a single SQLite file, `.onfly/vector.db`. Postgres with `pgvector` is optional, for shared or large indexes.

### Should I commit `.onfly/`?

Commit `config.yaml`, `rules/` and `plugins/`. Do not commit `vector.db`; add `.onfly/vector.db` to `.gitignore`. Each person (or CI) builds their own index.

## Search results

### Search returns nothing, or the wrong things

Check in this order:

1. `onfly stats`. If it says `files: 0`, nothing is indexed. See "My files are not indexed".
2. `onfly search "an exact function name from your code"`. If that works, the index is fine and your question was too vague. Try describing what the code does.
3. `onfly doctor`. A broken embedder falls back to keyword-only search, which works for exact words but not for meaning.

### My files are not indexed

A file must pass all three checks (see [Indexing](indexing.md)):

- It matches `indexer.include`.
- It is not in `indexer.exclude`, not git-ignored, and not a secret-looking name.
- A parser handles its extension.

`onfly index --dry-run` shows what would be indexed. The most common cause is a missing parser: `.go` files with no Go parser installed are skipped silently.

### Results are stale

The index only changes when you run `onfly index`. Use `onfly serve --watch` (or `onfly watch`) to keep it current while you work, or run `onfly index` in CI.

### Search is slow

The first search after starting the server loads the model. Repeated searches reuse cached query vectors. A hosted embedder adds a network round trip per search. A reranker adds another; remove the `reranker:` block if you do not need it.

### How do I know if a change made search better?

Write 20 or so real questions in `.onfly/eval.yaml`, then run `onfly eval`. It reports recall and MRR. Save a baseline with `--update-baseline` and compare later with `--baseline`. See [Evaluation](evaluation.md).

## Setup problems

### `embedder: ... connection refused`

Ollama is not running. Start it (`ollama serve`, or open the app) and check that the `endpoint` in the config is right.

### `dimension mismatch`

The index was built with a different embedding size than `embedder.dimensions` in your config. Either set `dimensions` back, or build a fresh index: delete `.onfly/vector.db` and run `onfly index`. Changing the model always needs a fresh index.

### `database file missing`

You have not indexed yet. Run `onfly index`.

### `parser type "stdio" ... set ONFLY_ALLOW_STDIO=1`

A `type: stdio` parser runs a shell command from your config. A cloned repo could name any command, so this is off until you opt in on your machine: `export ONFLY_ALLOW_STDIO=1`. WASM parsers are sandboxed and need no opt-in.

### `duplicate parser extension .php`

Two parsers claim the same extension. Either remove one, or give each an `include` scope so each owns part of the tree. See [per-parser scope](cookbook.md#use-a-different-parser-for-part-of-the-project).

### The IDE does not see Onfly

- The `command` in the MCP entry must be on the `PATH` the IDE uses, or be an absolute path.
- Pass `--config` with an absolute path if the IDE does not start in your project folder (`onfly mcp-config ... --config /abs/path/.onfly/config.yaml`).
- Check `.onfly/server.log`, which is where `serve` writes logs. Stdout is reserved for the protocol.

## Working in a team or monorepo

### Can several projects share one index?

Yes. Use one Postgres database and give each project a `workspace:` name in its config. Search takes a `workspace` argument, or `*` for all. See the [monorepo recipe](cookbook.md#index-a-monorepo).

### Do nested configs combine?

A `.onfly/config.yaml` in a sub-folder merges over its parent's, up to the git root. Include patterns are rebased onto the sub-folder, excludes are combined, and a child parser replaces the parent's parser for the same extension and scope.

## Still stuck?

Run `onfly doctor` and read each line. If you open an issue, include its output and your `config.yaml` (without keys).
