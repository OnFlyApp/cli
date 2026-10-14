# Configuration

Onfly reads `.onfly/config.yaml`. Starting from the working directory, it walks parent directories until it finds that file. `--config` skips the walk and opens the path you pass.

The project root is the parent of the `.onfly` directory that holds the file. Relative SQLite DSNs, rule paths, the log file, and WASM plugin paths are resolved from that root. A DSN of `:memory:`, an absolute path, or a string that contains `://` is left alone. Stdio `command` strings are expanded, then left relative to the process working directory.

`$VAR` and `${VAR}` are expanded (write `$$` for a literal `$`) in the DSN, embedder endpoint, model, API key, log file, rule paths, and parser path or command.

## File

```yaml
project: "my-project"

database:
  driver: sqlite                 # sqlite or postgres
  dsn: .onfly/vector.db          # postgres example: postgres://user:${ONFLY_DB_PASSWORD}@host:5432/onfly

workspace: backend               # optional; stored on each chunk from this config

embedder:
  provider: ollama               # ollama, openai, or voyage
  endpoint: http://localhost:11434
  model: qwen3-embedding:0.6b
  dimensions: 1024               # required, at least 1
  max_input_tokens: 6000         # default 6000; split chunks above this
  # api_key: ${OPENAI_API_KEY}   # openai falls back to OPENAI_API_KEY; voyage to VOYAGE_API_KEY

reranker:                        # optional, off by default (empty provider)
  provider: voyage               # voyage, cohere, jina, or ollama
  model: rerank-2                # for ollama: a chat model, e.g. qwen2.5-coder:7b
  # endpoint: http://localhost:8080   # optional; a self-hosted server, no key needed
  api_key: ${VOYAGE_API_KEY}     # falls back to VOYAGE_API_KEY, COHERE_API_KEY or JINA_API_KEY
  candidates: 30                 # fused hits sent to the reranker; default 30
  timeout_ms: 5000               # one rerank call; default 5000, 30000 for ollama; on timeout the fused order is used

search:                          # optional; how the result lists are fused
  semantic_weight: 1             # relative weights, 0 means 1
  keyword_weight: 1
  recency_weight: 0              # >0 adds a list of files changed in the last recency_days (git), newest first
  recency_days: 30

rules:
  - .onfly/rules/project.md

rules_engine:
  max_token_budget_percent: 15   # share of server.max_tokens used for injected rules
  auto_discovery:
    enabled: true                # when no explicit rule file exists, read the first fallback that is present
    fallback_order:
      - .cursorrules
      - AGENTS.md
      - CLAUDE.md
      - AGENT.md
      - .clinerules
      - .github/copilot-instructions.md

indexer:
  include: ["**/*"]
  exclude: [vendor, node_modules, dist, build, target, .venv]
  debounce_ms: 2000              # onfly watch; default 2000
  parse_timeout_ms: 5000         # per parser call; default 5000
  embed_context: false           # prefix each embedded chunk with its path and symbol; reindex after changing
  concurrency: 1                 # files indexed in parallel, 0 to 32; default sequential
  parsers:
    - extensions: [".php", ".inc"]
      type: wasm
      path: .onfly/plugins/php-parser.wasm
      include: ["app/**", "src/**"]    # optional; limits this parser to part of the project
      exclude: ["**/*Test.php"]        # optional; skipped by this parser only
    - extensions: [".go"]
      type: wasm
      path: .onfly/plugins/go-parser.wasm
    - extensions: [".md", ".blade.php"]
      type: stdio                # needs ONFLY_ALLOW_STDIO=1 in the environment
      command: php .onfly/plugins/custom_chunker.php

server:
  transport: stdio               # only stdio is accepted
  log_level: debug               # debug, info, warn, error
  log_file: .onfly/server.log    # required; serve writes logs here
  max_tokens: 8192               # search result budget; serve --max-tokens overrides when > 0
  watch: false                   # reindex changed files while serving; same as serve --watch
  # trace_file: .onfly/mcp_trace.jsonl   # off by default; appends one JSON line per tool call
```

If a `.git` directory or file sits at or above the start directory, Onfly loads every `.onfly/config.yaml` from the git root down to that directory and merges them. A set child scalar replaces the parent. Child `include` patterns are prefixed with the child path relative to the git root. The project root becomes the git root, and `workspace` is stored on each chunk. `--config` loads one file and does not merge. With no git root, only the nearest config is used.

These names are never read, even when `include` matches: any path segment that starts with `.env`, ends in `.pem`, or is `id_rsa`, `vendor`, `node_modules`, `.git`, or `.onfly`. The comparison ignores case, so `.ENV` and `.Onfly` are the same names. A `.gitignore` match is also skipped. `!` negation lines do not un-ignore a path. `read_file_raw` and `list_directory` refuse the same names.

`onfly init` writes a commented version of this file: `include: ["**/*"]`, a short `exclude` list, and the built-in `text` parser for `.md` and `.txt`. Language parsers are added with `onfly ext install`. Optional blocks (`workspace`, `search`, `reranker`, `concurrency`, `embed_context`) are present as comments.

`onfly ext install go` reads `https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json` (`ONFLY_REGISTRY_URL` overrides it), checks SHA-256, and appends a wasm parsers item. Existing comments stay. `onfly --config path ext install go` edits that file.

`indexer.include` must contain at least one pattern. Patterns use [doublestar](https://github.com/bmatcuk/doublestar) syntax: `src/**/*` matches `src/main.go` and `src/a/b.go`, and `docs/**/*.md` matches Markdown under `docs/`.

### Per-parser scope

Each parser may carry its own `include` and `exclude`. They are doublestar globs relative to the project root, applied after `indexer.include` and `indexer.exclude`:

- No `include` means every file the parser's extensions match.
- `exclude` removes files from this parser only.
- A file is routed to the parser with the longest matching extension whose scope accepts it. A file that no scope accepts is not indexed.
- Two parsers may share an extension when each has an `include`. Without one it is a configuration error.
- An invalid pattern fails when parsers are opened (`index`, `serve`, `doctor`). In a nested config they are prefixed with the child path, like `indexer.include`.

```yaml
indexer:
  parsers:
    - extensions: [.go]
      type: wasm
      path: .onfly/plugins/go-parser.wasm
      exclude: ["**/*_test.go", "**/testdata/**"]
```

### Exclude names

`indexer.exclude` matches a path segment. `vendor` skips `vendor/lib/a.go` and `src/vendor/a.go`. `.onfly` and `.git` are always skipped.

## Rules

Markdown files in `.onfly/rules/` and the paths in `rules` are the explicit rules. Each file may start with YAML frontmatter:

```markdown
---
name: Database Safety Rules
scope: ["**/*.go", "**/*.php", "interface"]
ignore: ["**/*_test.go", "vendor/**"]
priority: 100
---
Never concatenate SQL in {{ .Project.Name }}. Active parsers: {{ .ActiveParsers }}.
```

`scope` entries are doublestar globs, language names (`go`, `php`, `rust`, `typescript`, `javascript`, `python`, `ruby`, `java`), or chunk types such as `interface`. `*` and an empty scope apply to every MCP request. A file with no frontmatter does too. `ignore` uses the same glob syntax. A higher `priority` is sent first. A directory request matches a glob when the directory is that glob's literal prefix, so `frontend/src` matches `frontend/src/**`.

`{{ .Project.Name }}` comes from `go.mod`, then `composer.json`, then `package.json`, then `project`. `{{ .Env.OS }}` and `{{ .Env.Arch }}` are the host `GOOS` and `GOARCH`. `{{ .ActiveParsers }}` is a JSON list of languages derived from parser extensions, such as `["go","php"]`.

`rules_engine.max_token_budget_percent` defaults to 15 and must be from 1 to 100. Injected rules stop at that share of `server.max_tokens`. Lower-priority rules are dropped first. When a whole rule is dropped, the text ends with `// [Truncated: N rules omitted due to context limits.]`. `search`, `search_keyword`, and `get_references` fit their code results into the tokens that remain. `get_project_rules` returns only the rules that apply to every request. Path and chunk-type rules are on the tool result, in the text and in the structured `rules` field. Global rules are also in the server instructions, so they are present before a search.

`auto_discovery.enabled` defaults to true. When `.onfly/rules/` and `rules` are both empty, Onfly reads the first existing file in `fallback_order` and stops. Set `enabled` to false to ignore those files. Fallback paths stay inside the project root.

`onfly doctor` reads the same files. `rules: ok N tokens` means the rendered rules fit in the budget. A frontmatter or template error fails the check. When the rendered rules exceed the budget, doctor prints `rules: [WARNING]` and exits 0 if every other check passed. That warning names the files whose scopes overlap.

## Search fusion

`search` fuses the semantic and keyword lists by reciprocal rank, `weight / (60 + rank)` per list. `search.semantic_weight` and `search.keyword_weight` are relative; a zero value means 1. `search.recency_weight` above 0 adds a third list of candidates whose files were committed within `search.recency_days` (default 30), most recent first, read from `git log` in the project root and cached for 30 seconds. Outside a git repository the signal is skipped with a warning. Measure a change with [`onfly eval`](evaluation.md) before keeping it.

If the embedder is unreachable, `search` logs a warning and answers from keyword results alone instead of failing. A cancelled request still fails. Query vectors are cached in memory (256 entries), so a repeated query does not call the embedder again.

`indexer.embed_context: true` embeds `path: <file>` and `symbol: <Receiver.Name>` ahead of each chunk, so a query naming a file or function can match its vector. Stored content is unchanged. Chunks already in the index keep their old vectors until their file changes, so reindex from scratch (delete `.onfly/vector.db`) to apply it everywhere, and compare with `onfly eval --baseline`.

## Reranker

The reranker is off by default. When `reranker.provider` is set, each search sends the top fused hits to a rerank endpoint and reorders them. This adds one HTTP call per search. If the call fails or times out, search logs a warning and keeps the fused order. If the reranker is misconfigured (for example the API key is missing), the MCP server logs a warning and runs without it; `onfly doctor` prints `reranker: <error>` and fails. `onfly search` still returns the error.

| Provider | Default endpoint | Path | Key variable |
| --- | --- | --- | --- |
| `voyage` | `https://api.voyageai.com` | `/v1/rerank` | `VOYAGE_API_KEY` |
| `cohere` | `https://api.cohere.com` | `/v2/rerank` | `COHERE_API_KEY` |
| `jina` | `https://api.jina.ai` | `/v1/rerank` | `JINA_API_KEY` |

A hosted endpoint needs a key. With a custom `endpoint` the key is optional, so a self-hosted server works unauthenticated. Servers that copy the Cohere or Jina request shape (vLLM, llama.cpp with `--reranking`, Infinity, LiteLLM) can be used this way. Pick `cohere` or `jina` to match the path the server exposes.

### Ollama (LLM-based)

`provider: ollama` needs no rerank endpoint. Onfly sends the query and the candidates (each cut to 500 characters) to a chat model through `POST {endpoint}/api/chat` (default `http://localhost:11434`) and asks for a JSON ranking, with `temperature: 0` and `num_ctx: 8192`. One call covers all candidates.

```yaml
reranker:
  provider: ollama
  model: qwen2.5-coder:7b
  candidates: 20               # fewer candidates is faster and fits the context
  timeout_ms: 30000
```

Things to know:

- `model` is a chat model, not an embedding model. Use an instruction-tuned model of about 7B parameters or more; very small models often ignore the format.
- It is slower than a rerank endpoint, typically seconds per search on a laptop. The default timeout is 30 s, and on timeout or an unusable answer the fused order is kept.
- Numbers out of range, repeats, and extra text in the answer are ignored; candidates the model leaves out stay after the ranked ones in fused order.
- Everything stays on your machine, unlike the hosted providers.

Use `onfly eval` to check that it improves your results before keeping it.

## Embedders

Ollama: `POST {endpoint}/api/embed` with `{"model","input":[...]}`. The default endpoint is `http://localhost:11434`. The response field is `embeddings`. Each vector must have length `dimensions`.

OpenAI: `POST {endpoint}/v1/embeddings`. The default endpoint is `https://api.openai.com` (the client adds `/v1/embeddings`). The request sends `dimensions`. The API key is `embedder.api_key` if set, otherwise `OPENAI_API_KEY`.

Voyage: `POST {endpoint}/v1/embeddings`. The default endpoint is `https://api.voyageai.com`. The body sends `input_type` `query` for one search string and `document` for an index batch, and `output_dimension` from `dimensions`. The API key is `embedder.api_key` if set, otherwise `VOYAGE_API_KEY`.

The HTTP timeout is 60 seconds. Batches are 32 texts, or 128 with `onfly index --ci`. HTTP 429, 502, 503 and 504, and network errors, are retried up to five times. `Retry-After` of 1–30 seconds replaces the backoff. Other status codes, including a plain 500, fail immediately.

`dimensions` is stored in the database on first init. A later run with a different value fails and does not alter the schema. Point `dsn` at a new file or database if you change the model size.

## Database

`sqlite` opens a file with `modernc.org/sqlite` and sqlite-vec. The DSN gets a busy timeout of 5 seconds and WAL mode. One connection is used.

`postgres` uses `pgx` and expects the `vector` extension. The server must already be running. Onfly creates the `chunks` table, an HNSW index (`vector_cosine_ops`) when `dimensions` is at most 2000, and a GIN index on a stored `tsvector`. Wider vectors are scanned exactly.
