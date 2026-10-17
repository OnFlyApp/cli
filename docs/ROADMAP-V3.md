# Onfly v3 Roadmap: The AI-Native Backend Engineering Platform

## Overview
Onfly v3 evolves from a context compressor into a comprehensive, language-agnostic developer platform for AI agents and human engineers, maintaining our core principles: **CGO-free Go, sub-second cold starts, and zero mandatory external daemons.**

---

## The 6 Core Pillars of v3

### 1. Semantic Query Expander (`internal/query/expand.go`)
* **What:** Local SLM integration (`internal/slm`) to translate natural-language conceptual questions into precise technical keywords and synonyms.
* **Why:** Bridges the semantic gap between how humans ask questions and how variables/functions are named in code.

### 2. Polymorphic Interface Implementations (`internal/index` & `internal/mcp`)
* **What:** Indexes explicit `implements`/`extends` (PHP, TS, Rust) and **Go implicit interface satisfaction** (struct method set matching).
* **Why:** When inspecting an interface via `onfly_xray`, agents instantly see all concrete implementations across the codebase.

### 3. Architecture Rules Linter CLI (`onfly lint`)
* **What:** A local CLI command that parses `git diff`, matches changed lines/AST traits against the Rules Engine (.onfly/rules/), and enforces custom frontmatter constraints (e.g. `forbidden_calls`).
* **Why:** Extends architectural governance from AI prompt injection into CI/CD pipelines and local pre-commit hooks.

### 4. Context Efficiency Auditor (`onfly audit`)
* **What:** CLI diagnostic report combining SQLite telemetry (`telemetry_events`) and project file sizes.
* **Why:** Exposes where AI agents waste tokens (e.g. raw reads on large files that should use `onfly_xray`) and suggests optimization strategies.

### 5. Runtime & Test Interceptor (`onfly_run` MCP Tool)
* **What:** A macro-tool that executes test or build commands (`go test`, `phpunit`), strips stdout/stderr noise (progress bars, vendor stack traces), parses failures, and automatically injects the full AST code of the failed function.
* **Why:** Eliminates token burn from massive test failure logs and brings debugging context directly into the agent's window.

### 6. Environment & Usage Registry (`onfly_env` MCP Tool)
* **What:** Universal scanner for standard environment files (`.env`, `.env.example`, `.env.schema`) cross-referenced with AST-level references (`os.Getenv`, `env()`, etc.).
* **Why:** Gives agents an instant inventory of required environment variables and spots orphaned or missing configuration keys without parsing framework-specific configs.