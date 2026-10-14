---
name: Project
scope: ["*"]
priority: 10
---
# {{ .Project.Name }}

Go CLI (`github.com/axitdev/onfly`) that indexes a codebase and serves it to IDEs over MCP.

- Build with `CGO_ENABLED=0`. Do not add a dependency that needs cgo.
- Run tests with `go test ./... -skip TestE2EGoParser` (the e2e test clones a repo over the network). Run `go vet ./...` too.
- Every user-visible change updates the matching page in `docs/` and the README command table.
- Return errors with context (`fmt.Errorf("...: %w", err)`); do not panic in library code.
- `serve` owns stdout for JSON-RPC. Nothing else may write to stdout there; logs go to the log file or stderr.
- Stores (`internal/vectorstore`) must behave the same on SQLite and Postgres; add contract tests in `storetest` for new `Store` methods.
