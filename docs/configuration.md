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
  provider: ollama               # ollama, openai (any OpenAI-compatible server), or voyage
  endpoint: http://localhost:11434
  model: embeddinggemma-2
  dimensions: 768                # required, at least 1
  max_input_tokens: 6000         # default 6000; split chunks above this, and (Ollama) the context window requested
  # api_key: ${OPENAI_API_KEY}   # openai falls back to OPENAI_API_KEY (hosted endpoint only); voyage to VOYAGE_API_KEY
  # send_dimensions: true        # openai: ask the server to shorten vectors; default true for api.openai.com only
  # query_prefix: "task: code retrieval | query: "   # prepended to every search query
  # document_prefix: "title: none | text: "          # prepended to every indexed chunk; needs a fresh index
  # alt:                         # a second embedder; both vector lists are fused (needs a fresh index)
  #   provider: ollama
  #   model: qwen3-embedding:0.6b
  #   dimensions: 1024

reranker:                        # optional, off by default (empty provider)
  provider: voyage               # voyage, cohere, jina, or ollama
  model: rerank-2                # for ollama: a chat model, e.g. qwen2.5-coder:7b
  # endpoint: http://localhost:8080   # optional; a self-hosted server, no key needed
  api_key: ${VOYAGE_API_KEY}     # falls back to VOYAGE_API_KEY, COHERE_API_KEY or JINA_API_KEY
  candidates: 30                 # fused hits sent to the reranker; default 30
  timeout_ms: 5000               # one rerank call; default 5000, 30000 for ollama; on timeout the fused order is used

search:                          # optional; how the result lists are fused (values shown are the defaults)
  semantic_weight: 2             # relative list weights; 0 means the default
  keyword_weight: 0.5
  word_weight: 0.25              # per-word keyword list, relative to keyword_weight; 0 turns it off
  field_weight: 0.5              # symbol-name, path and comment list, relative to keyword_weight; 0 turns it off
  rrf_k: 20                      # reciprocal-rank constant; lower favours the top of each list
  recency_weight: 0              # >0 adds a list of files changed in the last recency_days (git), newest first
  recency_days: 30
  usage_weight: 0                # >0 adds a list of chunks an assistant opened after earlier searches
  section_weight: 1              # multiplier for documentation sections; below 1 demotes docs against code
  type_weights: {}               # score multiplier per chunk type, e.g. {trait_use: 0.3, property: 0.6}; unlisted types keep 1
  summary_word_weight: 0.125     # keyword list over summaries; 0 turns it off
  alt_weight: 1                  # list from the second embedder (embedder.alt)

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
  skip_types: [use]              # drop chunks of these types after parsing (see "onfly stats" for the types); reindex to apply
  embed_context: false           # prefix each embedded chunk with its path and symbol; reindex after changing
  # summaries: {provider: ollama, model: qwen2.5-coder:7b}   # one-sentence description per chunk, see below
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
  # usage_file: .onfly/usage.jsonl       # off by default; records which hit an assistant opens after a search
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

`search` ranks candidates in three steps.

1. **Retrieve.** Semantic (nearest vectors), keyword (the whole question as a substring) and a per-word keyword list (each content word of the question looked up on its own, chunks ranked by how many words they contain). The per-word list is what lets "retry on 429 from the embedder" find the chunk containing all three words.
2. **Name list.** The retrieved chunks are scored again by where the question's words appear: the symbol name (strongest), the file name, the directories, then the leading comment and the [summary](#chunk-summaries). A chunk *named* for the question outranks one that merely mentions it. Words match across plurals and endings (`retries`, `retry`; `index`, `indexing`). This list adds no store query.
3. **Fuse.** Every list scores `weight / (rrf_k + rank)` and the scores add up. Defaults: `semantic_weight` 2, `keyword_weight` 0.5, `word_weight` 0.25 and `field_weight` 0.5 (both relative to `keyword_weight`), `rrf_k` 20. A zero value takes the default, except `word_weight` and `field_weight`, where 0 turns the list off.
`search.type_weights` multiplies a chunk's fused score by its type (`onfly stats` lists the types). Use it where small declarations such as PHP `trait_use`, `property` or `enum_case` chunks match a question's words and outrank the class that holds them: a weight of 0.3 sends them below the definition without deleting them, unlike `indexer.skip_types`. A chunk with weight `w` contributes `w` of its reciprocal rank and takes only `w` of a position in each list, so the chunks behind it rank almost as if it were absent; a weight near 0 approaches deleting the type from search while keeping it findable. With any type weight set, each list also reads three times as many candidates (up to 150), because demoted chunks would otherwise fill the slots that real definitions need. It needs no reindex. Measure with `onfly eval` before keeping a value.

`search.recency_weight` above 0 adds another list of candidates whose files were committed within `search.recency_days` (default 30), most recent first, read from `git log` in the project root and cached for 30 seconds. Outside a git repository the signal is skipped with a warning.

The defaults were fitted with [`onfly eval tune`](evaluation.md#tuning) on half of 573 cases and checked on the other half, where MRR rose from 0.605 to 0.678. Your code may prefer other numbers: run `onfly eval tune` on your own cases before changing them.

If the embedder is unreachable, `search` logs a warning and answers from keyword results alone instead of failing. A cancelled request still fails. Query vectors are cached in memory (256 entries), so a repeated query does not call the embedder again.

`indexer.embed_context: true` embeds `path: <file>` and `symbol: <Receiver.Name>` ahead of each chunk, so a query naming a file or function can match its vector. Stored content is unchanged. Chunks already in the index keep their old vectors until their file changes, so reindex from scratch (delete `.onfly/vector.db`) to apply it everywhere, and compare with `onfly eval --baseline`.

### Query and document prompts

Some embedding models are trained with a task prompt in front of the text. EmbeddingGemma documents `task: code retrieval | query: ` for queries and `title: none | text: ` for documents. Onfly sends text unchanged unless you set them:

```yaml
embedder:
  query_prefix: "task: code retrieval | query: "
  document_prefix: "title: none | text: "
```

`query_prefix` goes in front of every search query (and expansion text), `document_prefix` in front of every chunk at index time, so changing the document prefix needs a fresh index (the summary cache keeps that cheap). On this repository the code-retrieval prompts raised MRR from 0.797 to 0.810, and the hand-written cases from 0.705 to 0.774. `task: search result | query: ` gave 0.806. Models without task prompts (Qwen3-Embedding takes an `Instruct: ... Query: ` prefix on the query side only) should be measured before setting any.

### Second embedder

`embedder.alt` adds a second embedding model. Each chunk is embedded by both, stored in a second vector slot, and search fuses the two vector lists (`search.alt_weight`, default 1). Models fail on different questions, so the union ranks better than either: embeddinggemma-2 with qwen3-embedding:0.6b as `alt` reached MRR 0.809 against 0.797 alone, and 0.816 together with the prompts above.

```yaml
embedder:
  model: embeddinggemma-2
  dimensions: 768
  alt:
    provider: ollama
    model: qwen3-embedding:0.6b
    dimensions: 1024
```

Only `provider`, `endpoint`, `model`, `dimensions`, `api_key`, `query_prefix`, `document_prefix` of `alt` are read. It costs a second embedding pass at index time and one more embedding per new query, and the index grows by one vector per chunk. Adding it to an existing database needs a reindex: delete `.onfly/vector.db` (the summary cache keeps the model work).

### Chunk summaries

`indexer.summaries` asks a chat model for one plain sentence per named chunk (functions, methods, types, doc sections of at least 60 characters). The sentence is stored as the chunk's `summary`, embedded together with the code, used by the name list, and returned in search results. It targets conceptual questions whose words never appear in the code ("wait before trying again" finding a backoff loop).

```yaml
indexer:
  summaries:
    provider: ollama             # ollama, or openai for vLLM, mlx_lm.server, LM Studio, llama.cpp, Msty, OpenAI
    # endpoint: http://localhost:8000   # openai: the server's base URL, without /v1
    # api_key: ${LLM_API_KEY}    # optional for a custom endpoint
    model: qwen2.5-coder:7b
    # endpoint: http://localhost:11434
    # timeout_ms: 60000          # one call; default 60000
    # concurrency: 8             # chunks of one file in flight at once (default 1)
    # files: true                # also write one summarised "file" chunk per file
    # context: true             # tell the model which symbols of the same file the chunk calls, is called by, sits next to
    # cache: .onfly/summaries.jsonl   # model answers kept across indexes; "none" turns it off
```

**Cache.** Every answer is also appended to `.onfly/summaries.jsonl`, keyed by the model, the prompt version, the chunk's type and name and its exact text. Deleting the database, changing the embedder or a prefix, or rebuilding on another machine that has the file therefore does not call the model again: a full reindex of this repository dropped from 17 minutes to about a minute and a half. A different summary model has different keys and asks again. Keep the file out of git like `vector.db`.

**Context.** `context: true` adds three lines to each prompt, built from the chunk's own file only: `Calls (same file)`, `Called by (same file)` and `Nearby`. A chunk's role often shows in who uses it, and the model otherwise sees only the body. The text is part of the cache key, so turning it on asks the model again for every chunk, and the cache entries made without it stay valid for when it is off. Cross-file callers are not used on purpose: they change as the index grows, which would invalidate summaries on every run. Measured on this repository it made no difference overall (MRR 0.810 against 0.816, within noise; hand-written cases 0.793 against 0.756) and took 20 minutes of model time, so it is off by default. Try it on a codebase whose files hold many small cooperating functions.

`files: true` adds a chunk of type `file` per file, listing the names it defines, with its own summary; it found no gain here (MRR 0.717 against 0.722 without) and is meant for large packages where the question is about a directory or file, not a symbol. A smaller model is the way to cut index time: `qwen2.5-coder:3b` indexed this repository in about two thirds of the time of the 7B for a loss of 0.004 MRR.

Summaries are written during indexing, one model call per new or changed chunk. A chunk whose text did not change keeps its stored summary, so a reindex calls the model only for what changed. The first index of a project is slower: on this repository (about 730 chunks) it took 16 minutes with a 7B model on a laptop instead of under a minute; it raised MRR from 0.663 to 0.751 in the [measurement](evaluation.md#measured-effect). If the model is unreachable, indexing logs a warning, skips the rest of that file's summaries and still completes. Turning summaries on does not touch chunks that are already indexed until their file changes; delete the database and reindex to apply them everywhere.

### Usage list

Off by default. It adds one more list to the fusion.

- `search.usage_weight` ranks hits that an assistant opened after an earlier search, most often first. It needs `server.usage_file`: with it set, the MCP server appends one JSON line (`ts`, `query`, `file`, `symbol`, `rank`) whenever a tool opens a chunk that the most recent `search` returned, within ten minutes of it. Nothing is recorded without it, and the file holds your questions in plain text, so keep it out of git. Read the file counts back as a prior with `usage_weight`, or turn it into eval cases with [`onfly eval mine`](evaluation.md#real-questions).

### Removed options

Earlier builds had `search.expand`, `search.group_by`, `search.dedupe`, `search.graph_weight`, `indexer.summaries.batch`, `indexer.summaries.questions`, `indexer.summary_vector` and `indexer.embed_neighbours`. Each was measured and none beat the defaults on the final configuration (see [evaluation](evaluation.md#removed-options)), so they were deleted. A config file that still names them loads; the keys are ignored.

## Other inference engines

Onfly needs three things from a model server: embeddings, short chat answers (summaries, judging), and optionally scores for a reranker. Ollama is the default for all three. Any server that speaks the **OpenAI API** can replace it, which covers vLLM, `mlx_lm.server`, LM Studio, llama.cpp's `llama-server`, Msty's local API and OpenAI itself. There is one setting per role and no engine-specific code:

| Role | Setting | Endpoint used |
| --- | --- | --- |
| Embeddings | `embedder.provider: openai` | `POST {endpoint}/v1/embeddings` |
| Summaries, `eval --judge` | `provider: openai` under `indexer.summaries` | `POST {endpoint}/v1/chat/completions` |
| Scoring reranker | `reranker.provider: openai-score` | `POST {endpoint}/v1/completions` with `logprobs` |
| Rerank endpoint (vLLM `/v1/rerank`) | `reranker.provider: cohere` or `jina` with `endpoint` | `POST {endpoint}/v2/rerank` or `/v1/rerank` |

`endpoint` is the server's base URL without `/v1`. An API key is optional for a custom endpoint (leave it out for a local server) and required for the hosted OpenAI endpoint. Two behaviours differ for custom endpoints: `dimensions` is not sent with embedding requests (most local servers reject it for models that cannot shorten vectors; set `embedder.send_dimensions: true` if yours accepts it), and chat requests carry `chat_template_kwargs: {"enable_thinking": false}`, which vLLM honours for reasoning models (servers that do not know it ignore it). Text inside `<think>...</think>` in an answer is removed.

### vLLM

```sh
vllm serve Qwen/Qwen3-Embedding-0.6B --task embed --port 8001     # embeddings
vllm serve Qwen/Qwen2.5-Coder-7B-Instruct --port 8002             # summaries, expansion
```

```yaml
embedder:
  provider: openai
  endpoint: http://localhost:8001
  model: Qwen/Qwen3-Embedding-0.6B
  dimensions: 1024

indexer:
  summaries:
    provider: openai
    endpoint: http://localhost:8002
    model: Qwen/Qwen2.5-Coder-7B-Instruct
    concurrency: 16              # vLLM batches requests: this is where the time goes
```

### mlx_lm.server (Apple silicon)

```sh
mlx_lm.server --model mlx-community/Qwen2.5-Coder-7B-Instruct-4bit --port 8080
```

```yaml
indexer:
  summaries:
    provider: openai
    endpoint: http://localhost:8080
    model: mlx-community/Qwen2.5-Coder-7B-Instruct-4bit
    concurrency: 4
```

`mlx_lm.server` serves chat models; keep an embedding model on Ollama, vLLM or LM Studio. Use the `model` name the server was started with.

### Summary concurrency

Without `indexer.summaries.concurrency` Onfly sends one chunk at a time per file, which suits Ollama's default. A server that batches requests answers many at once in nearly the time of one, so raising it shortens the first index in proportion, up to the server's limits. It multiplies with `indexer.concurrency` (files in parallel); keep the product within what the server accepts. The answers are applied in order and cached, so a failure or an interrupted run loses nothing already written.

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

### Ollama (scoring model)

`provider: ollama-score` is for generative reranking models such as Qwen3-Reranker. Ollama has no rerank endpoint, so Onfly sends each candidate to `POST {endpoint}/api/generate` as a raw prompt in the model's own format ("does the document answer the query? yes or no"), asks for one token with log-probabilities, and ranks candidates by the probability of `yes` against `no`. Calls run four at a time. It needs an Ollama version that returns `logprobs`.

```yaml
reranker:
  provider: ollama-score
  model: dengcao/Qwen3-Reranker-4B:Q4_K_M
  candidates: 20
  timeout_ms: 60000
```

`provider: openai-score` does the same through `POST {endpoint}/v1/completions` with `max_tokens: 1` and `logprobs: 20`, so a Qwen3-Reranker served by vLLM or `llama-server` works unchanged (`endpoint`, `model`; no key for a local server). Not every community build of the reranker works under every Ollama release: `dengcao/Qwen3-Reranker-0.6B:Q8_0` answered with identical probabilities for every token here, the 4B build worked. Check a few calls by hand before measuring. It is slow: about 18 seconds per search on a laptop for 20 candidates with the 4B model, and on the hand-written cases it did not improve MRR (0.768 against 0.767); see [Evaluation](evaluation.md#third-round-prompts-second-embedder-summary-variants).

## Embedders

Ollama: `POST {endpoint}/api/embed` with `{"model","input":[...]}`. The default endpoint is `http://localhost:11434`. The response field is `embeddings`. Each vector must have length `dimensions`.

OpenAI and compatible servers: `POST {endpoint}/v1/embeddings`. The default endpoint is `https://api.openai.com` (the client adds `/v1/embeddings`). On the hosted endpoint the request sends `dimensions` and the API key is `embedder.api_key` if set, otherwise `OPENAI_API_KEY` (required). With a custom `endpoint` (vLLM, LM Studio, llama.cpp, a gateway) no key is needed and `dimensions` is not sent unless `embedder.send_dimensions: true`; the returned vectors are still checked against `dimensions`. See [Other inference engines](#other-inference-engines).

Voyage: `POST {endpoint}/v1/embeddings`. The default endpoint is `https://api.voyageai.com`. The body sends `input_type` `query` for one search string and `document` for an index batch, and `output_dimension` from `dimensions`. The API key is `embedder.api_key` if set, otherwise `VOYAGE_API_KEY`.

The HTTP timeout is 60 seconds. Batches are 32 texts, or 128 with `onfly index --ci`. HTTP 429, 502, 503 and 504, and network errors, are retried up to five times. `Retry-After` of 1–30 seconds replaces the backoff. Other status codes, including a plain 500, fail immediately.

`embeddinggemma-2` (`ollama pull embeddinggemma-2`, 768 dimensions) was the best of six models tried here; see [Evaluation](evaluation.md#embedding-models).

**Context window.** Ollama loads an embedding model with a 4096-token window and cuts longer input without any error. `max_input_tokens` defaults to 6000, so Onfly sends `num_ctx: max_input_tokens + 512` with every Ollama embed request whenever that is above 4096, and `embedder.alt` inherits the value. Models that read fewer tokens than that (`nomic-embed-text` 2048, `embeddinggemma` v1 2048, `mxbai-embed-large` 512) cut the excess regardless; `onfly stats` and `onfly doctor` print a `hint:` when the configured model is one of them, and when the index holds chunks longer than about 2000 tokens. Set `max_input_tokens` to the model's context minus a margin and reindex so those chunks are split instead of cut. This repository has no chunk above 1900 tokens (median 98, 99th percentile 838), so it is unaffected.

`dimensions` is stored in the database on first init. A later run with a different value fails and does not alter the schema. Point `dsn` at a new file or database if you change the model size.

## Database

`sqlite` opens a file with `modernc.org/sqlite` and sqlite-vec. The DSN gets a busy timeout of 5 seconds and WAL mode. One connection is used.

`postgres` uses `pgx` and expects the `vector` extension. The server must already be running. Onfly creates the `chunks` table, an HNSW index (`vector_cosine_ops`) when `dimensions` is at most 2000, and a GIN index on a stored `tsvector`. Wider vectors are scanned exactly.
