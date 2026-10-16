
# Technical Specification: Onfly v2 Refactoring (Macro-MCP, Rule Piggybacking & Zero-Config DX)

## 1. Context & Objective
Scan the existing repository (`cmd/`, `internal/`, `pkg/`, `docs/`) to understand the current CGO-free Go architecture, WASM AST plugin system, SQLite/Postgres storage, Rules Engine, and MCP stdio server implementation.

**Problem Statement:**
Currently, Onfly exposes atomic MCP tools (e.g., `read_file_raw`, `list_directory`, `get_component_ast`, `search_semantic`) that directly compete with Claude Code's native primitives (`Read`, `Grep`, `Glob`). Consequently, the AI agent either ignores Onfly tools or wastes round-trips chaining small calls. Additionally, the `Rules Engine` requires explicit invocation, and initial setup creates too much developer friction.

**Objective:**
Refactor Onfly's MCP surface, Rules Engine delivery, local SLM pipeline, and CLI onboarding so that Onfly acts as a high-leverage **Context Compressor & Architectural Interceptor** rather than a redundant file reader.

---

## 2. Phase 1: Replace Atomic MCP Tools with 2 Macro-Tools

Deprecate/hide low-level atomic tools (`read_file_raw`, `list_directory`, `get_component_ast`, `search_semantic`, `search_keyword`) from the default MCP server registration. Replace them with two high-density macro-tools:

### Tool 1: `onfly_xray`
* **Description for LLM:** *"Inspect a file, directory, or symbol (class/struct/interface/function) with extreme token efficiency. Returns the AST skeleton (signatures & types without bulky bodies), its Blast Radius (inbound/outbound dependencies across the codebase), and mandatory architectural rules for that path. ALWAYS prefer `onfly_xray` over native `Read` when exploring unfamiliar files or files >100 lines."*
* **Input Schema:**
  * `target` (string, required): File path, directory path, or symbol name (e.g., `app/Services/PaymentService.php` or `PaymentService`).
  * `focus_symbol` (string, optional): Specific method/function name to keep fully expanded while collapsing the rest of the file into signatures.
  * `depth` (int, optional, default=1): Dependency graph traversal depth (1 = direct imports and direct callers).
* **Execution Pipeline:**
  1. Resolve `target` to file(s) or AST node(s) via SQLite index (if unindexed or stale, trigger Just-In-Time WASM parsing on the fly).
  2. **Skeletonize:** Render public/protected signatures, struct/class definitions, constants, and docblocks. Collapse method bodies (>10 lines) into `// ... [body omitted: lines X-Y, ~N tokens] ...` UNLESS the method matches `focus_symbol`.
  3. **Compute Blast Radius:** Query the AST index for:
     * `Outbound`: External types/methods referenced inside `target` (with their file paths and one-line signatures).
     * `Inbound`: Files/symbols across the repository that import or call `target`.
  4. **Rule Piggybacking:** Evaluate `target` file path and AST traits against the `Rules Engine` and append matching rules directly to the response payload.
  5. **Record Telemetry:** Calculate `raw_file_tokens - xray_response_tokens` and log the savings to SQLite telemetry table.

### Tool 2: `onfly_focus`
* **Description for LLM:** *"Hybrid architectural and semantic search across the codebase. Instead of returning a raw list of filenames, returns a ready-to-use context bundle: full code of the top matching symbols, AST skeletons of adjacent related structs/classes, and applicable project rules."*
* **Input Schema:**
  * `query` (string, required): Natural language intent or symbol keywords (e.g., `"webhook retry lock"` or `"CheckoutService validate"`).
  * `limit` (int, optional, default=5): Max primary symbols to expand.
* **Execution Pipeline:**
  1. Run hybrid search (SQLite FTS5 BM25 on symbol names, docblocks, and SLM-generated intents + vector similarity if enabled).
  2. For top $N$ matches: include full AST node body + file path + line numbers.
  3. For direct dependencies of those matches: include 1-line signatures.
  4. Piggyback matching rules from `Rules Engine`.
  5. Record token telemetry.

### Standardized Output Format for MCP Tools
Both tools MUST return compact, LLM-optimized Markdown/XML blocks:
```xml
<onfly_context target="app/Services/PaymentService.php" tokens_saved="3420">
  <skeleton>
    <!-- Pruned AST representation with line numbers -->
  </skeleton>
  <blast_radius>
    <calls>
      - Repository\TransactionRepository::lockForUpdate(int $id): bool [app/Repositories/TransactionRepository.php:45]
    </calls>
    <called_by>
      - Http\Controllers\WebhookController::handleStripe() [app/Http/Controllers/WebhookController.php:88]
    </called_by>
  </blast_radius>
  <architectural_rules>
    [CRITICAL RULE - services-layer.md]: Never call Eloquent models directly inside Services. Always use a Repository.
  </architectural_rules>
</onfly_context>

```

---

## 3. Phase 2: Forced Rule Piggybacking (Invisible Interceptor)

Modify the `Rules Engine` so it does not rely on the AI agent voluntarily calling a `get_rules` tool.

1. **Path & AST Matching:** Every time `onfly_xray` or `onfly_focus` touches a set of files, pass their relative paths and detected AST node types (e.g., `class`, `interface`, `struct`, `imports`) through the Rules Engine matcher.
2. **Deduplication per Session:** Keep an in-memory map in the MCP stdio server process (`injectedRules map[string]bool`) so the same architectural rule is injected at most once per MCP session (preventing token waste on repeated calls to the same directory).

---

## 4. Phase 3: Local SLM Integration (Optional Zero-Cost Enhancer)

Create a clean Go interface `internal/slm/Provider` supporting local HTTP endpoints (Ollama `/api/generate`, OpenAI-compatible local servers like `mlx_lm.server` or `llama-server`) with graceful fallback if no local model is running.

1. **Background AST-to-Intent Indexing:**
* When the file watcher or indexer processes a function/method AST node, if local SLM is enabled (`Qwen2.5-Coder-1.5B` / `Gemma-2-2B`), asynchronously generate a 1-sentence summary of the function's intent + 3-5 search keywords.
* Store this in a dedicated SQLite `fts5` virtual table (`symbol_intents_fts`) linked to the symbol's hash. Only re-compute when the AST node's content hash changes.


2. **Query-Aware Skeleton Pruning (in `onfly_xray`):**
* If a user/agent passes a natural language hint in `focus_symbol`, use the fast local SLM (or BM25 scoring over AST nodes in that file) to decide which methods in a large file to keep expanded and which to collapse.



---

## 5. Phase 4: Zero-Config DX & Telemetry CLI Commands

Refactor the CLI entrypoint so setup takes <5 seconds and provides immediate visual feedback on ROI:

1. **`onfly init` (Zero-Question Auto-Bootstrap):**
* Detect project languages automatically by scanning root manifests (`go.mod` -> Go, `composer.json` -> PHP, `package.json` -> TS/JS, `Cargo.toml` -> Rust, etc.).
* Automatically download missing required `.wasm` parsers in the background to `~/.onfly/plugins/`.
* Initialize `.onfly/index.db` (SQLite) and automatically append `.onfly/` to `.gitignore` if not present.
* Auto-discover existing instruction files (`.cursorrules`, `CLAUDE.md`, `.github/copilot-instructions.md`) for the Rules Engine.


2. **`onfly setup claude` (Seamless Agent Wiring):**
* Automatically register the `onfly mcp` stdio server in Claude Code's configuration (`.mcp.json` or via `claude mcp add` CLI wrapper).
* Inject or update a compact `<onfly_instructions>` block inside the project's `CLAUDE.md`:
```markdown
## Codebase Exploration (Onfly MCP)
- **MANDATORY:** Before using `Read` on any code file >100 lines, call `onfly_xray(target)` to inspect its skeleton, dependencies, and architectural rules at 10% of the token cost.
- Use `onfly_focus(query)` instead of multiple `Grep` calls when searching for business logic or unfamiliar flows.

```

3. **`onfly stats` (Token ROI & Telemetry):**
* Create a lightweight SQLite table `telemetry_events (timestamp, tool_name, target, raw_bytes, compressed_bytes, rules_injected_count)`.
* Running `onfly stats` renders a clean CLI summary table showing:
* Total MCP requests served.
* Estimated tokens saved (`(raw_bytes - compressed_bytes) / 4`) and % compression ratio.
* Estimated USD saved (based on standard Claude Sonnet input token pricing).
* Number of architectural rules automatically enforced.

---

## 6. Execution Plan for Claude Code

1. **Audit first:** Inspect the current codebase structure and existing MCP / AST / Storage packages.
2. **Propose a step-by-step migration plan** mapping these 4 phases to existing packages and structs without breaking CGO-free compilation.
3. **Implement Phase 1 (`onfly_xray` & `onfly_focus` + Rule Piggybacking) and Phase 4 (`telemetry` + `onfly setup claude`) first**, as they deliver 80% of the immediate value using the existing AST and Rules infrastructure.
4. Write unit/integration tests for the skeletonizer and MCP output formatting.

-