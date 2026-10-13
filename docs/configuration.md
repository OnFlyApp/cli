# Configuration

Onfly reads `.onfly/config.yaml`. Starting from the working directory, it walks parent directories until it finds that file. `--config` skips the walk and opens the path you pass.

The project root is the parent of the `.onfly` directory that holds the file. Relative SQLite DSNs, rule paths, the log file, and WASM plugin paths are resolved from that root. A DSN of `:memory:`, an absolute path, or a string that contains `://` is left alone. Stdio `command` strings are expanded, then left relative to the process working directory.

`$VAR` and `${VAR}` are expanded in the DSN, embedder endpoint, model, API key, log file, rule paths, and parser path or command.

## File

```yaml
project: "E-commerce CRM"

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

rules:
  - .onfly/rules/backend_style.md
  - .onfly/rules/frontend_style.md
  - .onfly/rules/architecture.md

indexer:
  include: ["src/**/*", "assets/**/*", "docs/**/*.md"]
  exclude: ["vendor", "node_modules", "var", "public"]
  debounce_ms: 2000              # onfly watch; default 2000
  parsers:
    - extensions: [".php", ".inc"]
      type: wasm
      path: .onfly/plugins/php-parser.wasm
    - extensions: [".go"]
      type: wasm
      path: .onfly/plugins/go-parser.wasm
    - extensions: [".md", ".blade.php"]
      type: stdio
      command: php .onfly/plugins/custom_chunker.php

server:
  transport: stdio               # only stdio is accepted
  log_level: debug               # debug, info, warn, error
  log_file: .onfly/server.log    # required; serve writes logs here
  max_tokens: 8192               # search result budget; serve --max-tokens overrides when > 0
  trace_file: .onfly/mcp_trace.jsonl
```

If a `.git` directory or file sits at or above the start directory, Onfly loads every `.onfly/config.yaml` from the git root down to that directory and merges them. A set child scalar replaces the parent. Child `include` patterns are prefixed with the child path relative to the git root. The project root becomes the git root, and `workspace` is stored on each chunk. `--config` loads one file and does not merge. With no git root, only the nearest config is used.

These names are never read, even when `include` matches: any path segment that starts with `.env`, ends in `.pem`, or is `id_rsa`, `vendor`, `node_modules`, `.git`, or `.onfly`. The comparison ignores case, so `.ENV` and `.Onfly` are the same names. A `.gitignore` match is also skipped. `!` negation lines do not un-ignore a path. `read_file_raw` and `list_directory` refuse the same names.

`onfly init` writes this shape with `parsers: []`. Add a parser with `onfly ext install`.

`onfly ext install go` reads `https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json` (`ONFLY_REGISTRY_URL` overrides it), checks SHA-256, and appends a wasm parsers item. Existing comments stay. `onfly --config path ext install go` edits that file.

`indexer.include` must contain at least one pattern. Patterns use [doublestar](https://github.com/bmatcuk/doublestar) syntax: `src/**/*` matches `src/main.go` and `src/a/b.go`, and `docs/**/*.md` matches Markdown under `docs/`.

`indexer.exclude` matches a path segment. `vendor` skips `vendor/lib/a.go` and `src/vendor/a.go`. `.onfly` and `.git` are always skipped.

## Embedders

Ollama: `POST {endpoint}/api/embed` with `{"model","input":[...]}`. The default endpoint is `http://localhost:11434`. The response field is `embeddings`. Each vector must have length `dimensions`.

OpenAI: `POST {endpoint}/v1/embeddings`. The default endpoint is `https://api.openai.com` (the client adds `/v1/embeddings`). The request sends `dimensions`. The API key is `embedder.api_key` if set, otherwise `OPENAI_API_KEY`.

Voyage: `POST {endpoint}/v1/embeddings`. The default endpoint is `https://api.voyageai.com`. The body sends `input_type` `query` for one search string and `document` for an index batch, and `output_dimension` from `dimensions`. The API key is `embedder.api_key` if set, otherwise `VOYAGE_API_KEY`.

The HTTP timeout is 60 seconds. Batches are 32 texts, or 128 with `onfly index --ci`. HTTP 429 and 503 are retried up to five times. `Retry-After` of 1–30 seconds replaces the backoff. Other status codes fail immediately.

`dimensions` is stored in the database on first init. A later run with a different value fails and does not alter the schema. Point `dsn` at a new file or database if you change the model size.

## Database

`sqlite` opens a file with `modernc.org/sqlite` and sqlite-vec. The DSN gets a busy timeout of 5 seconds and WAL mode. One connection is used.

`postgres` uses `pgx` and expects the `vector` extension. The server must already be running. Onfly creates the `chunks` table, an HNSW index (`vector_cosine_ops`) when `dimensions` is at most 2000, and a GIN index on a stored `tsvector`. Wider vectors are scanned exactly.
