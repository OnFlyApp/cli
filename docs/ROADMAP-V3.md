# Onfly v3 roadmap

Version 3 grows Onfly from a context compressor into a platform for AI agents and the people who work with them, under the same constraints as before: CGO-free Go, a cold start of about a second, and no mandatory external daemon.

Every item below ships behind a measurement. The retrieval benchmarks in [Evaluation](evaluation.md#large-repositories-and-other-languages) are the gate: a change that does not move a number, or that costs another one, is not kept. New MCP tools are opt-in, because each default tool costs context in every session.

## Status

All six pillars are implemented. Three are on by default or by a command (`onfly lint`, `onfly audit`, `onfly env` and the implementation lists in `onfly_xray`); `onfly_env` and `onfly_run` are opt-in tools (`server.tools`); the query expander is off by default because it did not pass its gate. The benchmark scripts of step 0 are in `scripts/bench`.

## The six pillars

### 1. Semantic query expander (`internal/query/expand.go`)

A local small language model (`internal/slm`) rewrites a conceptual question into the keywords and synonyms the code uses. It addresses the weakest case today: questions that do not repeat the names in the code.

An earlier build had query expansion and removed it (+0.016 MRR for about 3 s per new query). It is back as opt-in (`search.expansion`), once per query, and only when the top results are low-confidence. Status: implemented, off by default. With a small local model it did not lift the paraphrased-question set (Laravel 6/15 to 5/15), so it does not meet its own gate; see [Configuration](configuration.md#query-expansion-experimental).

### 2. Polymorphic interface implementations (`internal/mcp`)

Index explicit `implements` and `extends` (PHP already emits them; TypeScript and Rust need it) and Go's implicit interface satisfaction by matching struct method sets against interface method sets. `onfly_xray` on an interface then lists its implementations, with their callers.

Without type information the Go match is by method names, so it is reported as "likely" and requires every method of the interface. It is computed when `onfly_xray` is asked, from the chunk metadata and the method index, so it needs no new index and no reindex.

### 3. Architecture rules linter (`onfly lint`)

Evaluate `git diff` against the rules in `.onfly/rules/` and report violations of frontmatter constraints such as `forbidden_calls` and `forbidden_imports`, with file and line, an exit code, and `--format json`. It moves governance from prompt injection to CI and pre-commit hooks. The match is textual on the added lines, with strings and comments left out; a qualified entry such as `fmt.Println` is exact, a bare one does not match methods.

### 4. Context efficiency auditor (`onfly audit`)

A report on where tokens go: `telemetry_events` for Onfly calls, project file sizes, and, since Onfly cannot see an assistant's own `Read` and `Grep`, the assistant's transcripts (`--transcripts`, Claude Code first) to find raw reads of large files that `onfly_xray` would have covered, with suggestions.

### 5. Runtime and test interceptor (`onfly_run`)

A macro-tool that runs tests or a build (`go test`, `phpunit`, `pytest`), removes progress output and vendor stack traces, parses the failures and returns them with the code of the failing function. It reads structured output (`go test -json`, JUnit) rather than scraping logs.

It executes commands, so it is the one tool that is not read-only: an allowlist of commands in the config, no shell, a timeout, and a security review before release.

### 6. Environment and usage registry (`onfly_env`)

Read `.env`, `.env.example` and `.env.schema`, find the references in code (`os.Getenv`, `env()`, `process.env`, `std::env::var`), and report the variables a project needs, the ones used but not defined, and the ones defined but never used.

## Order

0. Put the benchmark scripts and pinned repositories in the repository so each pillar has a pass or fail gate (done: `scripts/bench`).
1. `onfly_env` and `onfly lint`: small, deterministic, no new dependency.
2. Interface implementations.
3. The query expander and the auditor.
4. `onfly_run`.

## Logging

All commands log through `log/slog` (`internal/logger`). `--verbose` lowers the level to debug and `--json-logs` writes one JSON record per line. Logs never go to standard output: `onfly serve` uses it for JSON-RPC, so its records go to `server.log_file`, and the other commands write to standard error.
