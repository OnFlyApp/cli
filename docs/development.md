# Development

Repository: <https://github.com/OnFlyApp/cli>. Module path: `github.com/axitdev/onfly`. The `go` line in `go.mod` is 1.27.1.

```sh
make build   # CGO_ENABLED=0 go build -o bin/onfly .
make test    # CGO_ENABLED=0 go test ./...
```

Postgres tests skip unless `ONFLY_TEST_POSTGRES_DSN` points at a disposable database. The test drops `chunks`, `schema_meta`, and `index_state` on that DSN. Do not aim it at a database you care about.

`schema_meta.version` is `2`. Opening a phase 1 SQLite file adds `outbound` and `workspace`, rewrites stored backslashes, and keeps the existing rows and dimensions. A stored version other than empty or `2` stops `Init` with `cannot be migrated` and leaves the row unchanged. `onfly doctor` pings the database and does not migrate.

`TestGoParserWASM` and `TestE2EGoParser` clone `OnFlyApp/ext-parser-go` with `gh` and build a WASI module.

## Layout

```text
main.go                 process entry
cmd/                    cobra commands: init, index, watch, serve, doctor, optimize, ext
internal/config/        YAML load, env expansion, git-root merge
internal/ext/           plugin registry, checksum download, lockfile
internal/ignore/        gitignore and secret-name blacklist
internal/parser/        chunk type, extension router, WASI host, stdio host
internal/tokens/        cl100k token counter
internal/split/         recursive chunk splitter
internal/embedder/      Ollama, OpenAI, and Voyage clients, 429 and 503 retry
internal/index/         hash skip, embed, save, prune, dry-run cost
internal/watch/         fsnotify debounce
internal/vectorstore/   sqlite and postgres, schema version 2
internal/mcp/           MCP tools, token budget, trace, index resource
```

## Store interface

`SaveChunks` takes `[]EmbeddedChunk`, each a `parser.Chunk`, a `[]float32`, and a workspace string. The store also has `DeleteFiles`, `ListFiles`, `SetFileHash`, `FileHashes`, `GetReferences`, `Generation`, and `Optimize`.

`Init` records `dimensions`. A different size returns `ErrDimensionMismatch` and leaves the tables in place.

SQLite is `modernc.org/sqlite` with a blank import of `modernc.org/sqlite/vec`. Vectors are written as JSON text (`[1,0,0]`), which is what this sqlite-vec build accepts. `ncruces/go-sqlite3` plus `sqlite-vec-go-bindings` does not load here: the vec WASM module uses atomics that wazero leaves disabled.

## Tests that lock behavior

- `onfly --help` contains the text `onfly index`.
- `.blade.php` is routed ahead of `.php`.
- A reindex does not duplicate chunks, and a deleted file is pruned.
- `serve` logs the line `onfly serve` in the log file and does not write it to stdout. Every stdout line of a session is JSON.
- `read_file_raw` rejects `..` and a symlink that leaves the project root.
- A second `Init` with a different dimension fails and the old rows remain.
