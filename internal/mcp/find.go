package mcp

import (
	"context"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type findArgs struct {
	Query     string `json:"query" jsonschema:"string, identifier or name pattern"`
	Kind      string `json:"kind,omitempty" jsonschema:"text or symbol; default symbol when query has * or ?"`
	Limit     int    `json:"limit,omitempty" jsonschema:"symbol results, 1 to 50"`
	Workspace string `json:"workspace,omitempty" jsonschema:"workspace name, or * for every workspace"`
}

func (h *tools) find(ctx context.Context, req *sdkmcp.CallToolRequest, in findArgs) (*sdkmcp.CallToolResult, any, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, nil, fmt.Errorf("query is required")
	}
	kind := in.Kind
	if kind == "" {
		kind = "text"
		if strings.ContainsAny(q, "*?") {
			kind = "symbol"
		}
	}
	switch kind {
	case "text":
		res, out, err := h.searchKeyword(ctx, req, keywordIn{Keyword: q, Workspace: in.Workspace})
		return res, out, err
	case "symbol":
		res, out, err := h.findSymbols(ctx, req, findIn{Pattern: q, Limit: in.Limit})
		return res, out, err
	}
	return nil, nil, fmt.Errorf("kind must be text or symbol, got %q", in.Kind)
}
