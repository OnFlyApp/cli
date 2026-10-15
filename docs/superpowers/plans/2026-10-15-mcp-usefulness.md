# MCP Usefulness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `onfly serve` cheaper and more useful to Claude Code: compact search results, tools Grep cannot replace (tests, history), and guidance that makes the model pick the right tool.

**Architecture:** All changes live in `internal/mcp` (new tools and result shaping), `internal/gitx` (one git helper) and `cmd` (one guidance command). No store or index changes, so no reindex and no `Store` interface change.

**Tech Stack:** Go, `github.com/modelcontextprotocol/go-sdk/mcp`, `git` CLI, existing `tokens.Count`.

**Spec:** none. This plan comes from the conversation of 2026-10-15 ("make the tool more useful": steps 2, 4, 5). Rulings below are provisional.

## Global Constraints

- `CGO_ENABLED=0`; no new dependencies.
- `go test ./... -skip TestE2EGoParser` and `go vet ./...` pass before every commit.
- Every user-visible change updates the matching page in `docs/` and the README command table.
- `serve` owns stdout; new code never writes to it.
- Errors carry context (`fmt.Errorf("...: %w", err)`); tool failures are tool errors, not protocol errors.
- File access goes through `SafePath` and the project's `ignore.Filter`; a tool never reads outside the project root.
- Commit messages end with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.

## Review Focus

- `search` with `detail` unset must stay valid for clients that ignore new fields (the default is now `auto`; old fields keep their names).
- A symbol with no tests returns an empty list, not an error.
- `get_tests` on a name that is a common word (`New`, `Run`) must be capped and say it was capped, not flood the context.
- `get_history` outside a git repository, or for a symbol with no line range, returns a clear tool error or a file-level fallback.
- `onfly guide --write` run twice leaves one block; it never touches text outside the markers.

## File map

| File | Change |
| --- | --- |
| `internal/mcp/server.go` | `detail` argument, `toHit` brief form, instructions text, tool descriptions, register two tools |
| `internal/mcp/tests.go` (new) | `get_tests` |
| `internal/mcp/history.go` (new) | `get_history` |
| `internal/gitx/gitx.go` | `LineLog` |
| `cmd/guide.go` (new) | `onfly guide [--write]` |
| `docs/mcp.md`, `docs/commands.md`, `README.md`, `docs/getting-started.md` | document all of the above |

---

### Task 1: Compact search results (`detail`)

`search` already returns the full chunk body in `lines` plus `summary`. Ten full bodies cost thousands of tokens, and the model usually needs two or three. Keep full bodies for the top hits and shrink the tail.

**Files:**
- Modify: `internal/mcp/server.go` (`semanticIn`, `finishSearch`, `toHits`, `toHit`, `hitDTO`)
- Test: `internal/mcp/server_test.go`

**Interfaces:**
- Produces: `semanticIn.Detail string` (`"auto"` default, `"full"`, `"brief"`); `hitDTO.Brief bool` (`json:"brief,omitempty"`); `func shapeHits(chunks []parser.Chunk, detail string) []hitDTO`.
- Consumes: existing `fitHits(chunks, maxTokens)`; change it to take `detail`.

Rules: `full` is today's behaviour. `brief` gives every hit `lines` = first line of the chunk (the signature) only. `auto` gives the first 3 hits full bodies and the rest brief. A brief hit has `Brief: true`, so the model knows to call `get_component_ast` for the body. `search_keyword` and the graph tools stay `full`.

- [ ] **Step 1: Write the failing test**

```go
func TestSearchDetailAutoShrinksTail(t *testing.T) {
	chunks := make([]parser.Chunk, 6)
	for i := range chunks {
		chunks[i] = parser.Chunk{FilePath: "a.go", Content: "func F() {\n\tbody()\n}", Metadata: map[string]string{"name": "F"}}
	}
	full := shapeHits(chunks, "full")
	auto := shapeHits(chunks, "auto")
	brief := shapeHits(chunks, "brief")
	for i := range chunks {
		if full[i].Brief || full[i].Lines != chunks[i].Content {
			t.Fatalf("full keeps bodies: %+v", full[i])
		}
	}
	if auto[2].Brief || !auto[3].Brief || auto[3].Lines != "func F() {" {
		t.Fatalf("auto keeps 3 bodies then signatures: %+v %+v", auto[2], auto[3])
	}
	if !brief[0].Brief {
		t.Fatalf("brief shrinks every hit: %+v", brief[0])
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/mcp -run TestSearchDetailAutoShrinksTail`
Expected: FAIL, `undefined: shapeHits`.

- [ ] **Step 3: Implement**

```go
const autoFullHits = 3

// shapeHits turns chunks into hits. "full" keeps every body; "brief" keeps
// only each chunk's first line; "auto" (and "") keeps the first autoFullHits
// bodies, so the tail costs a line each.
func shapeHits(chunks []parser.Chunk, detail string) []hitDTO {
	out := toHits(chunks)
	for i := range out {
		if detail == "full" || (detail != "brief" && i < autoFullHits) {
			continue
		}
		first, _, _ := strings.Cut(out[i].Lines, "\n")
		out[i].Lines, out[i].Brief = first, true
	}
	return out
}
```

Add `Brief bool \`json:"brief,omitempty"\`` to `hitDTO`, `Detail string \`json:"detail,omitempty" jsonschema:"auto (default): full code for the top 3 hits, signatures for the rest; full: every body; brief: signatures only"\`` to `semanticIn`. In `fitHits`, replace both `toHits(chunks[:k])` / `toHits(chunks)` calls with `shapeHits(..., detail)` and thread `detail` through; the graph tools and `search_keyword` pass `"full"`. Validate: any other `detail` value is a tool error.

- [ ] **Step 4: Run all mcp tests**

Run: `go test ./internal/mcp`
Expected: PASS. Fix any budget test that counted tokens of the old default by passing `Detail: "full"` there, and add one test asserting the `auto` payload is smaller than `full` for 10 multi-line chunks.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp && git commit -m "Shrink the tail of search results: detail auto|full|brief"
```

---

### Task 2: `get_tests`

Tests are excluded from the index on purpose (they crowd out code), so "what tests cover this symbol?" has no answer today. Answer it from the filesystem, not the index.

**Files:**
- Create: `internal/mcp/tests.go`, `internal/mcp/tests_test.go`
- Modify: `internal/mcp/server.go` (register tool)

**Interfaces:**
- Produces: tool `get_tests` with input `{name string, path string?}` and output `{rules?, tests: [{file_path, line, enclosing, snippet}], truncated bool}`; `func isTestFile(rel string) bool`; `func findTests(root string, filter *ignore.Filter, name string, limit int) ([]testHit, bool, error)`.
- Consumes: `SafePath`, `h.filter`, `h.root`, `ignore.Filter` (see `internal/ignore`).

Rules: a file is a test file when its slash path matches one of `*_test.go`, `test_*.py`, `*_test.py`, `*.test.{js,jsx,ts,tsx}`, `*.spec.{js,jsx,ts,tsx}`, `*Test.{php,java,kt}`, `*Tests.{cs,swift,kt,java}`, `*_spec.rb`, `*_test.rb`, `*_test.rs`, or has a directory named `tests`, `test`, `__tests__` or `spec`. A line matches when `name` appears with word boundaries. `enclosing` is the nearest preceding line that looks like a test declaration (`func Test`, `def test_`, `it(`, `test(`, `describe(`, `@Test`, `[Fact]`, `[Test]`, `#[test]`, `fn test_`, `func test`, `public function test`), else empty. Cap 20 hits, stop reading after 2000 files; set `truncated` when either cap hit. Skip files over 1 MiB. Optional `path` is a doublestar glob that limits which test files are scanned.

- [ ] **Step 1: Write the failing tests**

```go
func TestIsTestFile(t *testing.T) {
	for _, p := range []string{"a/b_test.go", "x/test_y.py", "web/a.test.ts", "web/a.spec.jsx", "src/FooTest.php", "pkg/tests/a.go", "app/__tests__/b.js"} {
		if !isTestFile(p) {
			t.Errorf("%s should be a test file", p)
		}
	}
	for _, p := range []string{"a/b.go", "contest/a.go", "latest.py"} {
		if isTestFile(p) {
			t.Errorf("%s should not be a test file", p)
		}
	}
}

func TestFindTestsReportsEnclosingTest(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a_test.go"), "package a\n\nfunc TestRetry(t *testing.T) {\n\tgot := Retry(3)\n\t_ = got\n}\n")
	write(t, filepath.Join(dir, "a.go"), "package a\nfunc Retry(n int) int { return n }\n")
	got, trunc, err := findTests(dir, nil, "Retry", 20)
	if err != nil || trunc || len(got) != 1 {
		t.Fatalf("%+v %v %v", got, trunc, err)
	}
	if got[0].File != "a_test.go" || got[0].Line != 4 || got[0].Enclosing != "func TestRetry(t *testing.T) {" {
		t.Fatalf("%+v", got[0])
	}
}

func TestFindTestsCapsHits(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a_test.go"), strings.Repeat("x := Run()\n", 50))
	got, trunc, _ := findTests(dir, nil, "Run", 20)
	if len(got) != 20 || !trunc {
		t.Fatalf("%d %v", len(got), trunc)
	}
}

func TestFindTestsNoMatchIsEmpty(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a_test.go"), "package a\n")
	got, trunc, err := findTests(dir, nil, "Nope", 20)
	if err != nil || trunc || len(got) != 0 {
		t.Fatalf("%+v %v %v", got, trunc, err)
	}
}
```

(`write` is a tiny `os.WriteFile` + `MkdirAll` helper added to the test file if the package has none.)

- [ ] **Step 2: Run to see them fail** — `go test ./internal/mcp -run 'IsTestFile|FindTests'` → FAIL, undefined.

- [ ] **Step 3: Implement** `tests.go`: `filepath.WalkDir(root)`, skip `.git` and directories the filter ignores, call `isTestFile(rel)`, read with `os.ReadFile` after a size check, scan lines with a precompiled `regexp.MustCompile("\\b" + regexp.QuoteMeta(name) + "\\b")`, remember the last declaration line with a second regexp. Register in `NewServer`:

```go
sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_tests", Description: "Find test files that mention a symbol, with the enclosing test and line. Use before changing a function to see what exercises it."}, h.getTests)
```

The handler validates `name` is non-empty, calls `findTests`, records a trace line like the other tools, and returns the structured result.

- [ ] **Step 4: Run all mcp tests** — `go test ./internal/mcp` → PASS.

- [ ] **Step 5: Commit** — `git commit -m "Add get_tests: find the tests that mention a symbol"`.

---

### Task 3: `get_history`

**Files:**
- Modify: `internal/gitx/gitx.go`, `internal/mcp/server.go` (register)
- Create: `internal/mcp/history.go`, `internal/mcp/history_test.go`
- Test: `internal/gitx/gitx_test.go` (create if absent)

**Interfaces:**
- Produces: `gitx.LineLog(ctx, root, file string, start, end, n int) ([]Commit, error)` with `type Commit struct{ Hash, Author, Date, Subject string }`; tool `get_history` with input `{name, receiver?, file_path?, limit?}` and output `{file_path, start_line, end_line, commits: []Commit}`.
- Consumes: the same definition lookup `get_callees` uses (`pickDefinition`, `h.store.GetComponents`); `lineNum`.

Rules: `git log -L start,end:file -n limit -s --format=%h%x09%an%x09%ad%x09%s --date=short`. Default limit 5, max 20. A definition without line numbers falls back to `git log -n limit -- file` (file level) and the result says so with `"scope": "file"`. Not a git repo or git missing: tool error `history needs a git repository`.

- [ ] **Step 1: Failing test for `LineLog`** — create a temp repo with `git init`, commit a file, change one function in a second commit, assert `LineLog` over that function's lines returns two commits newest first and over an untouched function returns one.
- [ ] **Step 2: Run** — `go test ./internal/gitx -run LineLog` → FAIL.
- [ ] **Step 3: Implement `LineLog`** using the existing `run` helper (it already trims and splits lines); split each line on `\t` into four fields; reject `file` that starts with `-`.
- [ ] **Step 4: Failing handler test** in `history_test.go`: seed a store with one chunk whose metadata has `start_line`/`end_line`, point `root` at a temp git repo, call the handler, assert one commit and `scope == "symbol"`; second case: chunk without lines → `scope == "file"`.
- [ ] **Step 5: Implement handler**, register with description `"Recent commits that touched a symbol's lines: who changed it, when, and why. Use to judge how stable or risky a function is."`.
- [ ] **Step 6: Run** — `go test ./internal/gitx ./internal/mcp` → PASS.
- [ ] **Step 7: Commit** — `git commit -m "Add get_history: commits that touched a symbol"`.

---

### Task 4: Guidance (server instructions, tool descriptions, `onfly guide`)

**Files:**
- Modify: `internal/mcp/server.go` (`instructions`, tool descriptions)
- Create: `cmd/guide.go`, `cmd/guide_test.go`
- Test: `internal/mcp/server_test.go`

**Interfaces:**
- Produces: `const guidance` in `internal/mcp` exported as `mcp.Guidance` (the same text is used for the server instructions and the CLAUDE.md block); `onfly guide` prints it; `onfly guide --write [--file CLAUDE.md]` inserts or replaces the block between `<!-- onfly:start -->` and `<!-- onfly:end -->`.

Guidance text (the whole block, kept under 120 words because it is read on every turn):

```
## Code search (onfly)
- Question about behavior or "where is X done": `search` first; Grep only for exact strings.
- Have a symbol name: `get_component_ast`, not Read of the whole file.
- Before changing a function: `get_impact` (callers) and `get_tests` (what covers it).
- Judging risk or why code looks odd: `get_history`.
- Search hits marked `brief` show only a signature; fetch the body with `get_component_ast`.
- If a result looks stale, the index may be behind: say so rather than guess.
```

- [ ] **Step 1: Failing tests**

```go
func TestGuideWriteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(f, []byte("# Mine\n\nkeep this\n"), 0o644)
	for i := 0; i < 2; i++ {
		if err := writeGuide(f); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(f)
	s := string(b)
	if strings.Count(s, "<!-- onfly:start -->") != 1 || !strings.Contains(s, "keep this") || !strings.HasPrefix(s, "# Mine") {
		t.Fatalf("%s", s)
	}
}

func TestGuideWriteReplacesOldBlock(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(f, []byte("a\n<!-- onfly:start -->\nold\n<!-- onfly:end -->\nb\n"), 0o644)
	if err := writeGuide(f); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	if strings.Contains(string(b), "old") || !strings.HasPrefix(string(b), "a\n") || !strings.HasSuffix(string(b), "b\n") {
		t.Fatalf("%s", b)
	}
}
```

Plus in `server_test.go`: assert the initialize result's instructions contain `get_impact` and that every registered tool description is non-empty and under 200 characters.

- [ ] **Step 2: Run** — `go test ./cmd -run Guide` → FAIL.
- [ ] **Step 3: Implement** `writeGuide` (read file or empty; if both markers exist replace between them, else append with a blank line; write atomically via temp file + rename), the `guide` cobra command, and prepend `Guidance` to `instructions` in `NewServer` (global rules text still goes first).
- [ ] **Step 4: Tighten tool descriptions**, each saying when to use it versus Grep or Read, for example `search`: "Find code by what it does when you do not know the identifier. Returns the symbol, summary and code of the best matches."
- [ ] **Step 5: Run** — `go test ./cmd ./internal/mcp` → PASS.
- [ ] **Step 6: Commit** — `git commit -m "Add onfly guide and routing guidance in the server instructions"`.

---

### Task 5: Docs and a real-use check

**Files:**
- Modify: `docs/mcp.md` (the `detail` argument, `get_tests`, `get_history`, guidance), `docs/commands.md` and `README.md` (`onfly guide`), `docs/getting-started.md` (run `onfly guide --write` after `mcp-config`)

- [ ] **Step 1: Update the pages** above; each new tool gets the same argument table the existing tools use.
- [ ] **Step 2: Full check** — `go vet ./... && go test ./... -skip TestE2EGoParser -count=1` → all pass.
- [ ] **Step 3: Real-use check (manual, this repo).** Set `server.trace_file` and `server.usage_file`; add the MCP entry with `onfly mcp-config claude-code > .mcp.json` and run `onfly guide --write`; give Claude Code three tasks (explain the fusion pipeline; add a field to `config.Search`; find what breaks if `Searcher.Params` changes). Record per task: tool calls to `onfly`, total result tokens from the trace file, and whether `get_tests`/`get_impact` were called. Write the numbers into `docs/evaluation.md` under a new "Agent sessions" heading, whatever they show.
- [ ] **Step 4: Commit** — `git commit -m "Document detail, get_tests, get_history and onfly guide"`.

---

## Self-review

- Step 2 (answers, not hits): Task 1. Note the finding that `search` already returned bodies and summaries; the change is shrinking the tail, not adding fields.
- Step 4 (tools Grep cannot replace): Tasks 2 and 3. Cross-file callers and "what breaks" already exist (`get_impact`, `get_callees`), so they are not re-planned.
- Step 5 (tell Claude when to use it): Task 4.
- Open ruling: `auto` becomes the default `detail`. If an existing client depended on full bodies for every hit, it passes `detail: "full"`. Cost if wrong: one extra `get_component_ast` call per tail hit.
