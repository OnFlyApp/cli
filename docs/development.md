# Development

Module path: `github.com/axitdev/onfly`. The `go` line in `go.mod` is 1.27.1.

```sh
make build   # CGO_ENABLED=0 go build -ldflags "-X .../internal/mcp.Version=$(git describe)" -o bin/onfly .
make test    # CGO_ENABLED=0 go test ./...
make vet     # go vet ./...
make lint    # vet, then staticcheck
```

CI runs `go vet`, `go test` on Linux, macOS and Windows, `go test -race`, `go mod tidy -diff`, `staticcheck`, and the vectorstore tests against a `pgvector/pgvector` service container.

Tests that run `type: stdio` parsers need `ONFLY_ALLOW_STDIO=1`; the packages that use them set it in a `TestMain`.

Postgres tests skip unless `ONFLY_TEST_POSTGRES_DSN` points at a disposable database. The test drops `chunks`, `schema_meta`, and `index_state` on that DSN. Do not aim it at a database you care about.

`schema_meta.version` is `2`. Opening a phase 1 SQLite file adds `outbound` and `workspace`, rewrites stored backslashes, and keeps the existing rows and dimensions. A stored version other than empty or `2` stops `Init` with `cannot be migrated` and leaves the row unchanged. `onfly doctor` pings the database and does not migrate.

`TestGoParserWASM` and `TestE2EGoParser` clone `OnFlyApp/ext-parser-go` with `gh` and build a WASI module.

`onfly search` uses the same pipeline as the MCP `search` tool, through `internal/query`. `onfly eval` (see [evaluation.md](evaluation.md)), `onfly stats`, and `onfly mcp-config` are the other query-side commands.

## Layout

```text
main.go                 process entry
cmd/                    cobra commands: init, index, watch, serve, search, eval, mcp-config, doctor, stats, optimize, ext
internal/config/        YAML load, env expansion, git-root merge
internal/ext/           plugin registry, checksum download, lockfile
internal/ignore/        gitignore, secret-name list, and Filter (the one scope check)
internal/parser/        chunk type, extension router, WASI host, stdio host
internal/tokens/        cl100k token counter
internal/split/         recursive chunk splitter
internal/embedder/      Ollama, OpenAI, and Voyage clients, retry on 429, 502, 503, 504 and network errors
internal/index/         hash skip, embed, save, prune, incremental ref linking, dry-run token count (walk.go: tree walk and symlink rules)
internal/watch/         fsnotify debounce, .gitignore reload
internal/vectorstore/   sqlite and postgres, schema version 2 (storetest/: fixtures for tests)
internal/mcp/           MCP tools, token budget, trace, index resource
internal/query/         one search: embed, semantic, keyword, rank fusion, optional rerank (5s bound)
internal/eval/          eval cases file, recall@k and MRR scoring
internal/rules/         rule frontmatter, templates, token budget, fallback files
```

## Store interface

`CommitFile` takes a path, a content hash, and `[]EmbeddedChunk` (each a `parser.Chunk`, a `[]float32`, and a workspace string). It replaces that file's chunks and records the hash in one transaction. The store also has `DeleteFiles`, `ListFiles`, `FileHash`, `FileHashes`, `ChunksByFiles`, `Names`, `ListChunks`, `UpdateOutbound`, `GetReferences`, `Generation`, and `Optimize`. Tests that need a populated store call `storetest.Save`.

`Init` records `dimensions`. A different size returns `ErrDimensionMismatch` and leaves the tables in place.

SQLite is `modernc.org/sqlite` with a blank import of `modernc.org/sqlite/vec`. Vectors are written as JSON text (`[1,0,0]`), which is what this sqlite-vec build accepts. `ncruces/go-sqlite3` plus `sqlite-vec-go-bindings` does not load here: the vec WASM module uses atomics that wazero leaves disabled.

## Tests that lock behavior

- `onfly --help` contains the text `onfly index`.
- `.blade.php` is routed ahead of `.php`.
- A reindex does not duplicate chunks, and a deleted file is pruned.
- `serve` logs the line `onfly serve` in the log file and does not write it to stdout. Every stdout line of a session is JSON.
- `read_file_raw` rejects `..` and a symlink that leaves the project root.
- A second `Init` with a different dimension fails and the old rows remain.
- A workspace-filtered semantic search still returns `limit` rows when another workspace owns the nearest vectors.
- Relinking after one file changes touches only that file's chunks unless the set of defined names changed.
- `read_file_raw` and `list_directory` refuse `.gitignore` matches, `indexer.exclude` names, and secret names.
- Keyword search matches a substring inside an identifier, and an index built with the default FTS tokenizer is rebuilt on `Init`.
