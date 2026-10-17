# Cookbook

Short recipes. Each one is a complete answer to a "how do I...".

## Use OpenAI or Voyage instead of Ollama

```yaml
embedder:
  provider: openai                  # or voyage
  model: text-embedding-3-small
  dimensions: 1536
  api_key: ${OPENAI_API_KEY}        # or just export OPENAI_API_KEY
```

`dimensions` must match what the model returns. Switching models means a fresh index: delete `.onfly/vector.db` and run `onfly index`.

## Use vLLM or mlx_lm.server for summaries

Point the chat roles at any OpenAI-compatible server and let it batch:

```yaml
indexer:
  summaries:
    provider: openai
    endpoint: http://localhost:8000
    model: Qwen/Qwen2.5-Coder-7B-Instruct
    concurrency: 16
```

Keep embeddings where they are, or add a second `openai` endpoint for them. Full recipes for vLLM and `mlx_lm.server` are in [Other inference engines](configuration.md#other-inference-engines).

## Only index part of the project

Index just the backend and docs:

```yaml
indexer:
  include: ["backend/**", "docs/**"]
  exclude: [vendor, node_modules, dist, build]
```

`exclude` takes folder or file names that are skipped anywhere in the tree. Your `.gitignore` files are honoured too, so build output is usually already out.

## Skip noisy files

`onfly stats` and `onfly doctor` print a `hint:` when data or generated files take a big share of the index:

```text
hint: .json files are 38% of indexed text (212 files); consider adding them to indexer.exclude
```

Two ways to act on it. Skip the folder:

```yaml
indexer:
  exclude: [fixtures, testdata]
```

Or leave out the extension by not giving it a parser (a file with no parser is never indexed).

## Quiet small declarations (properties, enum cases, imports)

Some parsers emit one chunk per `use` line, property or enum case. Their names often match a question's words and outrank the class that holds them. Look at `onfly stats` for the chunk types, then:

```yaml
indexer:
  skip_types: [use, namespace]       # no search value at all: drop them (reindex)
search:
  type_weights: {trait_use: 0.05, property: 0.1, enum_case: 0.1}   # keep them findable, rank them low (no reindex)
```

Dropping the types outright removes those lookups, which this keeps. Change one type at a time and run `onfly eval` after each.

## Use a different parser for part of the project

Each parser can have its own `include` and `exclude`, on top of the global ones. Paths are relative to the project root.

Skip tests in the Go parser:

```yaml
indexer:
  parsers:
    - extensions: [.go]
      type: wasm
      path: .onfly/plugins/go-parser.wasm
      exclude: ["**/*_test.go"]
```

Two parsers for the same extension, split by folder:

```yaml
indexer:
  parsers:
    - extensions: [.php]
      type: wasm
      path: .onfly/plugins/php-modern.wasm
      include: ["app/**"]
    - extensions: [.php]
      type: wasm
      path: .onfly/plugins/php-legacy.wasm
      include: ["legacy/**"]
```

Rules: a file goes to the first parser whose scope accepts it, with the longest extension winning (`.blade.php` over `.php`). Two parsers can share an extension only when both have an `include`.

## Index docs and config files too

The text parser splits Markdown by heading and other text by block:

```yaml
indexer:
  parsers:
    - extensions: [.md, .mdx]
      type: text
    - extensions: [.yaml, .yml, .toml]
      type: text
      include: ["config/**", "deploy/**"]
```

## Index a monorepo

One config at the repo root, with a language parser per area:

```yaml
project: shop
workspace: shop
indexer:
  include: ["services/**", "web/**", "docs/**"]
  parsers:
    - extensions: [.go]
      type: wasm
      path: .onfly/plugins/go-parser.wasm
      include: ["services/**"]
    - extensions: [.md]
      type: text
```

Or one config per package. A `.onfly/config.yaml` inside `services/billing/` merges over the root one. Give each a `workspace:` name, point them all at one Postgres database, and filter at search time:

```sh
onfly search "refund flow" --workspace billing
onfly search "refund flow" --workspace '*'        # every workspace
```

## Share one index with the team (Postgres)

```yaml
database:
  driver: postgres
  dsn: postgres://onfly:${ONFLY_DB_PASSWORD}@db.internal:5432/onfly
```

The database needs the `vector` extension (`CREATE EXTENSION vector;`). Everything else is created for you. A CI job indexes on every push to main:

```sh
onfly init --ci        # writes .github/workflows/onfly-index.yml
```

## Keep the index fresh

| Situation | Use |
| --- | --- |
| Working in an IDE | `onfly serve --watch`, or `server.watch: true` |
| Editing without the assistant | `onfly watch` in a spare terminal |
| Shared or CI-built index | `onfly index --ci` on every push |

## See only what changed on this branch

```sh
onfly search "error handling" --changed-since main
```

The assistant gets the same option through the `changed_since` argument of `search` (with `server.atomic_tools`). Handy for reviews.

## Search only code, or only docs

Docs headings can outrank code for broad questions. Narrow by chunk type or path:

```sh
onfly search "how are secrets handled" --type function     # code only (also: method, struct)
onfly search "how are secrets handled" --type section      # Markdown only
onfly search "retry policy" --path 'internal/**'
```

The assistant gets the same options as the `type` and `path` arguments of `search` (with `server.atomic_tools`).

## Improve ranking with a reranker

```yaml
reranker:
  provider: voyage            # or cohere, jina
  model: rerank-2
  api_key: ${VOYAGE_API_KEY}
  timeout_ms: 3000
```

It reorders the top results with a stronger model. It costs one extra request per search; if it fails or times out, you get the normal order.

To rerank locally, run a rerank server (for example llama.cpp with `--reranking`, or vLLM) and point at it. No key is needed:

```yaml
reranker:
  provider: jina              # the request shape the server speaks
  model: bge-reranker-v2-m3
  endpoint: http://localhost:8081
```

## Fail a pull request that breaks a convention

Put the constraint in a rule:

```markdown
---
name: No debug output
scope: ["**/*.go"]
ignore: ["**/*_test.go"]
forbidden_calls: [fmt.Println]
message: Log with slog.
---
Log with slog; do not print.
```

Then run `onfly lint` where you want it. As a pre-commit hook it checks the staged lines, in CI the pull request's:

```sh
onfly lint --staged                       # .git/hooks/pre-commit
onfly lint --base origin/main --format json   # CI
```

## Let the assistant run your tests

`onfly_run` starts nothing that is not listed. Allow the test commands and switch the tool on:

```yaml
server:
  tools: [onfly_run]
run:
  commands: ["go test", "vendor/bin/phpunit"]
  timeout_seconds: 180
```

The assistant gets the failures, with the code of the functions they point at, not a screen of log. See [`onfly_run`](mcp.md#onfly_run-optional).

## Find out where tokens go

```sh
onfly audit                    # telemetry, large files read whole, tips
onfly audit --since 7          # the last week
onfly env --strict             # exit 1 if code reads a variable no .env file defines
```

## Tell if a change helped

Create `.onfly/eval.yaml` with real questions:

```yaml
cases:
  - query: where is the content hash computed
    expect:
      - file: internal/index/index.go
        symbol: ContentHash
  - query: how do we retry failed embedding calls
    expect:
      - file: internal/embedder/embedder.go
```

Then:

```sh
onfly eval --update-baseline --baseline .onfly/eval-baseline.json   # record today's score
# ...change config, parser or weights, reindex...
onfly eval --baseline .onfly/eval-baseline.json                      # fails if it got worse
```

## Add a project rule

Put a file in `.onfly/rules/` and list it in the config:

```markdown
---
name: Error handling
scope: ["**/*.go"]
priority: 50
---
Return errors; do not panic in request handlers. Wrap with fmt.Errorf("...: %w", err).
```

```yaml
rules:
  - .onfly/rules/project.md
  - .onfly/rules/errors.md
```

Assistants then see it next to any Go code Onfly returns. More in [Rules](rules.md).

## Install a parser you built

```sh
onfly ext install ./my-parser.wasm --extensions=".twig,.html"
```

Or run a script (needs `export ONFLY_ALLOW_STDIO=1`):

```yaml
indexer:
  parsers:
    - extensions: [.twig]
      type: stdio
      command: php tools/twig_chunker.php
```
