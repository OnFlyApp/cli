# Rules

Rules are short Markdown notes that Onfly adds to MCP results, so an assistant sees your conventions next to the code it asked for, without calling a tool to fetch them. `onfly_xray` and `onfly_focus` match each rule against the paths and chunk types of what they return and append the matches in `<architectural_rules>`. A rule is sent once per MCP session: the server remembers, in memory, which rules it has sent, so a second call about the same directory carries none and a restart sends them again. `onfly init` writes one starter file, `.onfly/rules/project.md`, and lists it under `rules` in the config. Add more files and list them there.

A rule file can start with YAML frontmatter:

```markdown
---
name: Database Safety Rules
scope: ["**/*.go", "interface"]
ignore: ["**/*_test.go"]
priority: 100
---
Never concatenate SQL in {{ .Project.Name }}. Active parsers: {{ .ActiveParsers }}.
```

`scope` takes a doublestar glob, a language name (`go`, `php`, `rust`, `typescript`, `javascript`, `python`, `ruby`, `java`), or a chunk type such as `interface`. `*` applies to every MCP call. A file with no frontmatter does too. Higher `priority` is sent first. `ignore` uses the same glob syntax.

`{{ .Project.Name }}` comes from `go.mod`, then `composer.json`, then `package.json`, then the `project` field. `{{ .Env.OS }}` and `{{ .Env.Arch }}` are the host system. `{{ .ActiveParsers }}` is a JSON list of languages taken from the configured parser extensions.

Injected rules use at most `rules_engine.max_token_budget_percent` (default 15) of `server.max_tokens`. Lower-priority rules are dropped. `onfly doctor` fails when frontmatter or a template cannot be read. When the rules are larger than that budget it prints `rules: [WARNING]` and exits 0 if the other checks passed.

With no file in `.onfly/rules/` and an empty `rules` list, serve reads the first existing path in `auto_discovery.fallback_order`. The default order starts `.cursorrules`, `AGENTS.md`, `CLAUDE.md`, `AGENT.md`. Set `auto_discovery.enabled` to false to skip those files. Global rules are in the server instructions. Matching rules are also a `rules` field on the tool result. The field list is in [docs/configuration.md](configuration.md).

## Constraints checked by `onfly lint`

A rule can also forbid code. `onfly lint` reads these frontmatter keys and reports the lines a change adds that break them:

```markdown
---
name: No debug output
scope: ["**/*.go", "**/*.php"]
ignore: ["**/*_test.go"]
forbidden_calls: [fmt.Println, dd, var_dump]
forbidden_imports: [github.com/pkg/errors]
severity: error          # or warning
message: Log with slog; do not print.
---
```

`forbidden_calls` entries are matched as text on one line, with strings and comments left out. A qualified entry (`fmt.Println`, `Foo::bar`, `$this->x`) is exact. A bare one (`dd`) matches a call that is not a method and not qualified, so `$this->dd()` and `Foo::dd()` are not flagged; a declaration `function dd(` is, since it looks like a call. `forbidden_imports` match the import forms of Go, PHP `use`, JavaScript and TypeScript `from` and `require`, and Python `import` and `from`, for the path and the paths below it. There is no type information, so a method that shares the name of a forbidden function in another package is not told apart; use the qualified form.

`scope` and `ignore` select files as for injection. The body of the rule is still injected into tool results, so the same text can tell the assistant and check its work. See [Commands](commands.md).

