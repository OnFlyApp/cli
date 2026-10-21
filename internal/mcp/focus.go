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
	defaultFocus = 5
	maxFocus     = 20
	maxFocusDeps = 12
	maxFocusRefs = 60

	baselineFocusFiles = 3
	focusBodyLimit     = 6000
)

type focusIn struct {
	Query string `json:"query" jsonschema:"what you are looking for: the concept in words plus likely code-level synonyms and verbs, e.g. retry release attempts backoff, or expiration expire ttl timeout"`
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
	if err := h.warm(ctx, ""); err != nil {
		return nil, struct{}{}, err
	}
	limit := in.Limit
	if limit < 1 {
		limit = defaultFocus
	}
	limit = min(limit, maxFocus)
	top, err := h.searcher.RunFocus(ctx, query.Request{Query: in.Query, Limit: limit, Workspace: h.scope("")})
	if err != nil {
		return nil, struct{}{}, err
	}
	if len(top) > limit {
		top = top[:limit]
	}
	inTop := map[string]bool{}
	var b strings.Builder
	b.WriteString("## symbols\n")
	for _, c := range top {
		inTop[c.Metadata["name"]+"|"+c.FilePath] = true
		body := c.Content
		if len(body) > focusBodyLimit {
			body = body[:focusBodyLimit] + "\n// ... [truncated] ..."
		}
		fmt.Fprintf(&b, "### %s @ %s\n%s\n", c.Metadata["name"], location(c), body)
	}
	var rel strings.Builder
	defs, src := h.referencedDefinitions(ctx, top, maxFocusRefs, h.newSources())
	n := 0
	for _, ref := range src.order {
		cands := defs[ref]
		if len(cands) == 0 || n >= maxFocusDeps {
			continue
		}
		d := nearestDefinition(cands, src.by[ref][0])
		if inTop[d.Metadata["name"]+"|"+d.FilePath] {
			continue
		}
		fmt.Fprintf(&rel, "- %s [%s]\n", signature(d, h.lim().signatureClip), location(d))
		n++
	}
	if rel.Len() > 0 {
		b.WriteString("## related\n" + rel.String())
	}
	// Without onfly the assistant would read the files of the best three matches.
	raw, counted := 0, map[string]bool{}
	for _, c := range top {
		if len(counted) == baselineFocusFiles {
			break
		}
		if !counted[c.FilePath] {
			counted[c.FilePath] = true
			raw += h.fileSize(c.FilePath)
		}
	}
	text, nRules := h.rulesOnce(chunkQuery(append([]parser.Chunk(nil), top...)))
	body := cutToBudget(b.String(), h.maxTokens)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return h.finishMacro(ctx, "onfly_focus", in.Query, raw, fmt.Sprintf("query=%q", in.Query), body, text, nRules), struct{}{}, nil
}
