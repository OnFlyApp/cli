package mcp

// Guidance tells a model which onfly tool to reach for. The server sends it as
// instructions, and "onfly guide" prints it for a CLAUDE.md or AGENTS.md. It is
// read on every turn, so it stays short.
const Guidance = `## Code search (onfly)
- Question about behavior, or "where is X done": call search first; use Grep only for exact strings.
- Have a symbol name: get_component_ast, not Read of the whole file.
- Before changing a function: graph (callers) and get_tests (what covers it).
- To judge risk or why code looks odd: get_history.
- Search hits marked brief show only a signature; fetch the body with get_component_ast.
- If a result looks stale, the index may be behind: say so rather than guess.`
