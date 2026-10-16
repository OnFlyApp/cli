package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultFocus   = 5
	maxFocus       = 20
	maxFocusDeps   = 12
	maxFocusRefs   = 60
	focusBodyLimit = 6000
)

type focusIn struct {
	Query string `json:"query" jsonschema:"what you are looking for: behavior in words or symbol keywords"`
	Limit int    `json:"limit,omitempty" jsonschema:"symbols to expand with full code; default 5"`
}

func (h *tools) focus(ctx context.Context, req *sdkmcp.CallToolRequest, in focusIn) (*sdkmcp.CallToolResult, struct{}, error) {
	start := time.Now()
	res, out, err := h.runFocus(ctx, req, in)
	h.record("onfly_focus", in, start, resultText(res), 0)
	return res, out, err
}

func (h *tools) runFocus(ctx context.Context, _ *sdkmcp.CallToolRequest, in focusIn) (*sdkmcp.CallToolResult, struct{}, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, struct{}{}, fmt.Errorf("query is required")
	}
	limit := in.Limit
	if limit < 1 {
		limit = defaultFocus
	}
	limit = min(limit, maxFocus)
	top, err := h.searcher.Run(ctx, query.Request{Query: in.Query, Limit: limit, Workspace: h.scope("")})
	if err != nil {
		return nil, struct{}{}, err
	}
	if len(top) > limit {
		top = top[:limit]
	}
	files := map[string]bool{}
	inTop := map[string]bool{}
	var b strings.Builder
	b.WriteString("<symbols>\n")
	for _, c := range top {
		files[c.FilePath] = true
		inTop[c.Metadata["name"]+"|"+c.FilePath] = true
		body := c.Content
		if len(body) > focusBodyLimit {
			body = body[:focusBodyLimit] + "\n// ... [truncated] ..."
		}
		fmt.Fprintf(&b, "<symbol name=%q at=\"%s\">\n%s\n</symbol>\n", c.Metadata["name"], location(c), body)
	}
	b.WriteString("</symbols>\n<related>\n")
	defs, src := h.referencedDefinitions(ctx, top, maxFocusRefs)
	n := 0
	for _, ref := range src.order {
		cands := defs[ref]
		if len(cands) == 0 || n >= maxFocusDeps {
			continue
		}
		d := nearestDefinition(cands, src.by[ref])
		if inTop[d.Metadata["name"]+"|"+d.FilePath] {
			continue
		}
		files[d.FilePath] = true
		fmt.Fprintf(&b, "- %s [%s]\n", signature(d), location(d))
		n++
	}
	b.WriteString("</related>\n")
	raw := 0
	for f := range files {
		raw += h.fileSize(f)
	}
	text, nRules := h.rulesOnce(chunkQuery(append([]parser.Chunk(nil), top...)))
	body := cutToBudget(b.String(), h.maxTokens)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return h.finishMacro(ctx, "onfly_focus", in.Query, raw, fmt.Sprintf("query=%q", in.Query), body, text, nRules), struct{}{}, nil
}
