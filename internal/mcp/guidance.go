package mcp

const Guidance = `## Code search (onfly)
- Question about behavior, or "where is X done": call search first; use Grep only for exact strings.
- Have a symbol name: get_component_ast, not Read of the whole file.
- Before changing a function: graph (callers) and get_tests (what covers it).
- To judge risk or why code looks odd: get_history.
- Search hits marked brief show only a signature; fetch the body with get_component_ast.
- A hit marked stale changed on disk after indexing: Read the file before trusting its lines.`
