# Indexing

`onfly index` loads the config, opens every parser, and walks the project root.

A file is indexed when all of these hold:

- It matches an `indexer.include` pattern.
- It is not excluded by `indexer.exclude`, a `.gitignore` match, or the secret-name list in [configuration](configuration.md).
- A parser claims its suffix.
- It is a regular file of at most 2 MiB.

Directory symlinks are not followed. A file symlink is read only when its target stays inside the project and is not excluded, gitignored, or a secret name. The walker does not read files that have no parser.

Each selected file is hashed with xxHash64. A stored hash that matches is skipped: the parser and the embedder are not called. Otherwise the file is parsed, split when a chunk is over `embedder.max_input_tokens` (the same number sizes the context window requested from Ollama), embedded, and stored in one transaction. The split overlap is 12% of that limit. The stored `file_path` uses forward slashes and is relative to the project root.

## Without the embedder

Parsing and storing a file need no network. `onfly index --lexical`, `onfly init`, and the MCP tools on a missing or edited file store the chunks without vectors: name, text, path and reference search work at once, and `onfly_xray` is complete. Such a file carries the suffix `+lex` on its stored hash, so the next `onfly index` that can embed does not take it for unchanged, keeps the vectors of chunks whose text is the same, and embeds the rest. A plain `onfly index` does the same when the embedder fails: a refused connection is not retried, the embedder is not asked again for a minute, and each remaining file is stored without vectors. The summary says how many (`N files are stored without vectors`). `onfly serve` starts that embedding in the background at startup when it finds such files, and after it builds an empty index or syncs an edited one.

A lexical run parses with one worker per core (at most 8) unless `indexer.concurrency` is set.

## Generated files

A file is left out, and removed from an existing index, when its base name matches one of the `generated` globs of its language, or when its first kilobyte holds one of the `generated_headers` markers. The built-in rules are `zz_generated*`, `*.pb.go` and `*.pb.gw.go` for Go, `_ide_helper*.php` and `.phpstorm.meta.php` for PHP, and, for every language, `Code generated ... DO NOT EDIT` and `@generated`. In Kubernetes (`pkg`, `cmd`, `apiserver`) that was 9,500 of 54,000 chunks. Both lists are set per parser ([language rules](configuration.md#language-rules)); `indexer.index_generated: true` indexes the files anyway.

## Directives

A statement that is only `declare(strict_types=1);` is not a chunk: one per PHP file was 5,455 of the 38,521 chunks of a billing monorepo, they are returned by `onfly_focus` as a symbol named `statements`, and their short text embeds close to any question that names the file. The words come from `ignore_directives` of the parser entry (default `[declare]` for PHP). A statement that holds a `declare` and other code is kept.

## File summaries

Besides its symbols, each file gets one chunk of type `file_summary`: its path, namespace, the types it declares and the names of its functions and methods (the first 40), for example

```
File src/Illuminate/Queue/Worker.php
Namespace: Illuminate\Queue
Types: Worker
Defines: daemon, runNextJob, process, handleJobException, markJobAsFailedIfWillExceedMaxAttempts, calculateBackoff ...
```

It is embedded alone, with the usual `path:` and `kind:` lines. A question about what a file is for shares words with that list, not with any one method body, so the summary finds the file when the symbols do not. `onfly_focus` never returns it as such: it replaces it by the file's best-matching method ([ranking](configuration.md#ranking-in-onfly_focus)). Documents and files with nothing named in them get none; a lexical index (`onfly index --lexical`) gets none, since a summary is for the vectors.

Summaries are written when a file is indexed. `onfly index` also adds them to files indexed before they existed, without touching the other chunks, their vectors or the file's hash: one embedding per file (a 5,000-file project takes a few minutes). On the benchmark suites they took the monorepo's recall@5 from 0.43 to 0.57 and added 0.05 to Hugo's and the Onfly repository's symbol suites, with no loss elsewhere.

## Speed

On a laptop, lexical: Go 2,000 files and 12,000 chunks in 13 s; Go 4,700 files and 44,000 chunks in 44 s; PHP 3,200 files and 56,000 chunks in 5 minutes (the PHP parser is the cost, about 0.6 s of CPU per file). The index of the PHP project is 112 MB, of the Go one 208 MB. Embedding is far slower: 12,000 chunks took 20 minutes with `qwen3-embedding:0.6b`. The compiled form of a WASM parser is cached in the user cache directory (`onfly/wasm`), which saves about a second per process.

The default embed batch is 32 (`embedder.batch_size`). `onfly index --ci` uses 128. A request is given `embedder.timeout_seconds` (120); a batch that times out is split in halves and asked again, down to one chunk, instead of being sent whole six times, which is what happens when a one-slot server meets 128 long chunks. A WASM or stdio parse that runs longer than `indexer.parse_timeout_ms` is cancelled. With no value set, a WASM parse may take 5 seconds plus 150 ms per KiB of source, so a 75 KB PHP file gets 16 seconds; a set value is used as given. With `indexer.concurrency` above 1, that many files are parsed, embedded and committed in parallel; each file is still committed in its own transaction. Each WASM runtime is limited to 256 MiB.

## Reindex

A successful parse replaces that file's chunks and writes the new hash. A file that now returns no chunks is deleted and the new hash is kept.

A file that fails to stat, read, or parse keeps its previous chunks and its previous hash. The walk continues. If any file failed, the process still prunes other paths, prints the summary, and exits 1.

After a finished walk, stored paths that were not seen and did not fail are deleted. That removes files you deleted, files that are now ignored, and files that grew past 2 MiB. An embedder or store error returns before that prune.

Opening the database or a dimension mismatch stops the run.

Stderr names the current step. On a terminal it stays on one line:

```text
[######--------------] 12/73 parsing internal/mcp/server.go
```

The steps are `loading tokenizer`, `opening database`, `loading parsers`, `walking`, then `N/Total` with `parsing`, `summarising` (only with [`indexer.summaries`](configuration.md#chunk-summaries)), `embedding`, `unchanged`, or `failed`, then `pruning` and `linking references`. A pipe or a log file gets one line per step and one line per file for its outcome (`embedding`, `unchanged`, or `failed`), and no bar. Warnings read `warning: parse path=src/a.go err=...`. Per-file warnings are logged there too. The run ends with:

```text
indexed 12 files, 40 chunks, reused 25, skipped 3, pruned 2, failed 1
```

`reused` counts chunks whose stored vector was kept because the chunk text is unchanged in a file that was edited; only the other chunks are embedded. A vector is reused when the text is identical and the dimension matches, so switching to a different model with the same dimension keeps old vectors for unchanged text. Run `onfly index` against a new database after changing the model.

With `indexer.summaries` set, each new or changed chunk also costs one chat-model call before it is embedded; see [Chunk summaries](configuration.md#chunk-summaries). A chunk whose text is unchanged keeps its stored summary and its vector.

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

SQLite keeps text, metadata, `outbound`, and `workspace` in `chunk_meta`, generated summaries in a trigram `summary_fts` table, the optional extra vector (`embedder.alt`) in one `chunk_vecs_<slot>` table each (Postgres: an `emb_<slot>` column), the content hash in `index_state`, vectors in a `vec0` table, and an FTS5 index maintained by insert and delete triggers. Semantic search uses sqlite-vec `MATCH` with `distance_metric=cosine`. `Init` rewrites an existing L2 `vec0` table in place and leaves the schema version at `2`. After the parser returns, the indexer fills `metadata.start_line` and `metadata.end_line` when the parser left them empty, using the first match of the chunk text in the file, searching forward from the previous chunk so repeated snippets get successive lines. Keyword search is a case-insensitive substring match. The FTS5 table uses the `trigram` tokenizer, so keywords of three or more characters are answered from the index; shorter ones scan `chunk_meta`. An index created by an older build is rebuilt with trigrams on the next `Init`.

Postgres keeps one `chunks` row with a `vector(N)` column and a stored `tsvector`. Semantic search orders by cosine distance (`<=>`). An HNSW index is created when `N` is at most 2000. Wider vectors are scanned exactly. Keyword search is `ILIKE` or `plainto_tsquery('simple', ...)`.

After indexing, outbound lists are filled from symbol names used as `Name(` or `.Name` in the chunk text. The chunk's own name is not added. An outbound value that contains `.` or `\` is kept when that text is still in the chunk.

`onfly_xray` (given a symbol name) and the atomic `get_component_ast` look up `metadata.name`. Split parts of that symbol in the same file are joined in part order, and the shared overlap is kept once. A name stored in several files returns the earliest row.
