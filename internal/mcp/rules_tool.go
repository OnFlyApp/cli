package mcp

import (
	"context"
	"path/filepath"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/rules"
	"github.com/axitdev/onfly/internal/tokens"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (h *tools) projectRules(context.Context, *sdkmcp.CallToolRequest, emptyIn) (*sdkmcp.CallToolResult, rulesOut, error) {
	start := time.Now()
	out, err := h.projectRulesBody()
	h.record("get_project_rules", emptyIn{}, start, out, 0)
	return nil, out, err
}

func (h *tools) projectRulesBody() (rulesOut, error) {
	if h.engine == nil {
		return rulesOut{}, nil
	}
	if err := h.engine.ReadError(); err != nil {
		return rulesOut{}, err
	}
	return rulesOut{Rules: h.engine.Inject(rules.Query{}, h.maxTokens)}, nil
}

func (h *tools) rulesText(q rules.Query) string {
	if h.engine == nil {
		return ""
	}
	return h.engine.Inject(q, h.maxTokens)
}

func (h *tools) fitBudget(q rules.Query) (int, string) {
	text := h.rulesText(q)
	if text == "" {
		return h.maxTokens, ""
	}
	n, err := tokens.Count(text)
	if err != nil || n <= 0 {
		return h.maxTokens, text
	}
	if n >= h.maxTokens {
		return 1, text
	}
	return h.maxTokens - n, text
}

func chunkQuery(chunks []parser.Chunk) rules.Query {
	q := rules.Query{Targets: make([]rules.Target, 0, len(chunks))}
	for _, chunk := range chunks {
		q.Targets = append(q.Targets, rules.Target{Path: chunk.FilePath, Type: chunk.Type})
	}
	return q
}

func requestPath(root, p string) string {
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(root, p); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}
