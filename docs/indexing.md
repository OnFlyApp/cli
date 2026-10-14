# Indexing

`onfly index` loads the config, opens every parser, and walks the project root.

A file is indexed when all of these hold:

- It matches an `indexer.include` pattern.
- It is not excluded by `indexer.exclude`, a `.gitignore` match, or the secret-name list in [configuration](configuration.md).
- A parser claims its suffix.
- It is a regular file of at most 2 MiB.

Directory symlinks are not followed. A file symlink is read only when its target stays inside the project and is not excluded, gitignored, or a secret name. The walker does not read files that have no parser.

Each selected file is hashed with xxHash64. A stored hash that matches is skipped: the parser and the embedder are not called. Otherwise the file is parsed, split when a chunk is over `embedder.max_input_tokens`, embedded, and stored in one transaction. The split overlap is 12% of that limit. The stored `file_path` uses forward slashes and is relative to the project root.

The default embed batch is 32. `onfly index --ci` uses 128. A WASM or stdio parse that runs longer than `indexer.parse_timeout_ms` (default 5 seconds) is cancelled. With `indexer.concurrency` above 1, that many files are parsed, embedded and committed in parallel; each file is still committed in its own transaction. Each WASM runtime is limited to 256 MiB.

## Reindex

A successful parse replaces that file's chunks and writes the new hash. A file that now returns no chunks is deleted and the new hash is kept.

A file that fails to stat, read, or parse keeps its previous chunks and its previous hash. The walk continues. If any file failed, the process still prunes other paths, prints the summary, and exits 1.

After a finished walk, stored paths that were not seen and did not fail are deleted. That removes files you deleted, files that are now ignored, and files that grew past 2 MiB. An embedder or store error returns before that prune.

Opening the database or a dimension mismatch stops the run.

Stderr names the current step. On a terminal it stays on one line:

```text
[######--------------] 12/73 parsing internal/mcp/server.go
```

The steps are `loading tokenizer`, `opening database`, `loading parsers`, `walking`, then `N/Total` with `parsing`, `embedding`, `unchanged`, or `failed`, then `pruning` and `linking references`. A pipe or a log file gets one line per step and no bar. Per-file warnings are logged there too. The run ends with:

```text
indexed 12 files, 40 chunks, reused 25, skipped 3, pruned 2, failed 1
```

`reused` counts chunks whose stored vector was kept because the chunk text is unchanged in a file that was edited; only the other chunks are embedded. A vector is reused when the text is identical and the dimension matches, so switching to a different model with the same dimension keeps old vectors for unchanged text. Run `onfly index` against a new database after changing the model.

A normal index leaves stdout empty.

`onfly index --dry-run` walks, ignores, parses, splits, and counts tokens. It does not open the database and does not call the embedder. Stdout is:

```text
files: 12
chunks: 40
tokens: 18000
```

## Watch

`onfly watch` watches the project, except `.git` and `.onfly`, and waits `indexer.debounce_ms` (default 2000) before reindexing one path. Writes inside `.onfly` do not call the parser. A removed file or directory is dropped after the same delay. Editing a `.gitignore` reloads the ignore rules, drops files that became ignored, and reindexes files that became allowed. Reference links are recomputed only for the changed file unless the set of defined names changed.

`onfly serve --watch` (or `server.watch: true`) runs the same watcher inside the MCP server process. It loads the parsers and the embedder there. Watcher errors go to the server log, not stdout.

## What gets stored

SQLite keeps text, metadata, `outbound`, and `workspace` in `chunk_meta`, the content hash in `index_state`, vectors in a `vec0` table, and an FTS5 index maintained by insert and delete triggers. Semantic search uses sqlite-vec `MATCH` with `distance_metric=cosine`. `Init` rewrites an existing L2 `vec0` table in place and leaves the schema version at `2`. After the parser returns, the indexer fills `metadata.start_line` and `metadata.end_line` when the parser left them empty, using the first match of the chunk text in the file, searching forward from the previous chunk so repeated snippets get successive lines. Keyword search is a case-insensitive substring match. The FTS5 table uses the `trigram` tokenizer, so keywords of three or more characters are answered from the index; shorter ones scan `chunk_meta`. An index created by an older build is rebuilt with trigrams on the next `Init`.

Postgres keeps one `chunks` row with a `vector(N)` column and a stored `tsvector`. Semantic search orders by cosine distance (`<=>`). An HNSW index is created when `N` is at most 2000. Wider vectors are scanned exactly. Keyword search is `ILIKE` or `plainto_tsquery('simple', ...)`.

After indexing, outbound lists are filled from symbol names used as `Name(` or `.Name` in the chunk text. The chunk's own name is not added. An outbound value that contains `.` or `\` is kept when that text is still in the chunk.

`get_component_ast` looks up `metadata.name`. Split parts of that symbol in the same file are joined in part order, and the shared overlap is kept once. A name stored in several files returns the earliest row.
