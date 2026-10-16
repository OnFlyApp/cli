package mcp

const Guidance = `## Codebase exploration (onfly)
- Before Read on a code file over 100 lines, call onfly_xray(target): skeleton, callers and callees, and the project rules for that path at a fraction of the tokens. Pass focus_symbol to keep one method expanded.
- To find a flow or business logic, call onfly_focus(query) instead of several Grep calls: full code of the best matches, signatures of what they use, and the rules.
- Before changing a function: onfly_xray (called_by) and get_tests. To judge risk: get_history.
- Rules arrive with these results once per session; do not ask for them separately.`
