package mcp

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	"github.com/axitdev/onfly/internal/tokens"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (h *tools) search(ctx context.Context, req *sdkmcp.CallToolRequest, in semanticIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	res, out, err := h.runSearch(ctx, req, in, start)
	if err != nil {
		h.record("search", in, start, nil, 0)
	}
	return res, out, err
}

func (h *tools) runSearch(ctx context.Context, req *sdkmcp.CallToolRequest, in semanticIn, start time.Time) (*sdkmcp.CallToolResult, hitList, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, hitList{}, fmt.Errorf("query is required")
	}
	if in.Workspace == "" {
		ws, asked, err := workspaceChoice(req)
		if err != nil {
			return nil, hitList{}, err
		}
		if asked {
			names, err := h.store.WorkspacesForName(ctx, in.Query)
			if err != nil {
				return nil, hitList{}, err
			}
			if !slices.Contains(names, ws) {
				return nil, hitList{}, fmt.Errorf("workspace %q is not in the index", ws)
			}
			return h.finishSearch(ctx, in, ws, start)
		}
		if componentName.MatchString(in.Query) {
			names, err := h.store.WorkspacesForName(ctx, in.Query)
			if err != nil {
				return nil, hitList{}, err
			}
			if len(names) >= 2 {
				return &sdkmcp.CallToolResult{
					InputRequests: sdkmcp.InputRequestMap{
						"workspace": &sdkmcp.ElicitParams{
							Message: fmt.Sprintf("Which workspace should be searched for %q?", in.Query),
							RequestedSchema: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"workspace": map[string]any{"type": "string", "enum": names},
								},
								"required": []string{"workspace"},
							},
						},
					},
				}, hitList{}, nil
			}
		}
	}
	return h.finishSearch(ctx, in, h.scope(in.Workspace), start)
}

func (h *tools) finishSearch(ctx context.Context, in semanticIn, workspace string, start time.Time) (*sdkmcp.CallToolResult, hitList, error) {
	switch in.Detail {
	case "", "auto", "full", "brief":
	default:
		return nil, hitList{}, fmt.Errorf("detail must be auto, full or brief, got %q", in.Detail)
	}
	fused, err := h.searcher.Run(ctx, query.Request{Query: in.Query, Limit: in.Limit, Workspace: workspace, ChangedSince: in.ChangedSince, Type: in.Type, Path: in.Path})
	if err != nil {
		return nil, hitList{}, err
	}
	budget, text := h.fitBudget(chunkQuery(fused))
	hits, err := fitHits(fused, budget, in.Detail)
	if err != nil {
		return nil, hitList{}, err
	}
	h.markStale(ctx, hits)
	logged := in
	logged.Workspace = workspace
	h.record("search", logged, start, hits, len(hits))
	out := hitList{Rules: text, Hits: hits}
	if out.Hits == nil {
		out.Hits = []hitDTO{}
	}
	return contentWithRules(text, out), out, nil
}

func workspaceChoice(req *sdkmcp.CallToolRequest) (string, bool, error) {
	if req == nil || req.Params == nil || len(req.Params.InputResponses) == 0 {
		return "", false, nil
	}
	raw, ok := req.Params.InputResponses["workspace"]
	if !ok {
		return "", false, nil
	}
	er, ok := raw.(*sdkmcp.ElicitResult)
	if !ok || er == nil || er.Action != "accept" {
		return "", false, fmt.Errorf("workspace is required")
	}
	ws, _ := er.Content["workspace"].(string)
	if ws == "" {
		return "", false, fmt.Errorf("workspace is required")
	}
	return ws, true, nil
}

func (h *tools) searchKeyword(ctx context.Context, _ *sdkmcp.CallToolRequest, in keywordIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	chunks, err := h.store.SearchKeyword(ctx, in.Keyword, keywordLimit, h.scope(in.Workspace))
	if err != nil {
		h.record("find", in, start, nil, 0)
		return nil, hitList{}, err
	}
	budget, text := h.fitBudget(chunkQuery(chunks))
	hits, err := fitHits(chunks, budget, "full")
	h.markStale(ctx, hits)
	h.record("find", in, start, hits, len(hits))
	out := hitList{Rules: text, Hits: hits}
	if out.Hits == nil {
		out.Hits = []hitDTO{}
	}
	return contentWithRules(text, out), out, err
}

func truncationText(omitted int) string {
	return fmt.Sprintf("// [Truncated: %d matches omitted due to context limits.]", omitted)
}

func diagnosticHit(omitted int) hitDTO {
	return hitDTO{Lines: truncationText(omitted)}
}

func fitPrefix(n, maxTokens int, size func(k int) (int, error)) (int, error) {
	lo, hi := 0, n
	for lo < hi {
		mid := (lo + hi + 1) / 2
		sz, err := size(mid)
		if err != nil {
			return 0, err
		}
		if sz <= maxTokens {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, nil
}

func fitHits(chunks []parser.Chunk, maxTokens int, detail string) ([]hitDTO, error) {
	n := len(chunks)
	k, err := fitPrefix(n, maxTokens, func(k int) (int, error) {
		payload := shapeHits(chunks[:k], detail)
		if k < n {
			payload = append(payload, diagnosticHit(n-k))
		}
		return tokens.Count(mustJSON(payload))
	})
	if err != nil {
		return nil, err
	}
	switch {
	case k == n:
		return shapeHits(chunks, detail), nil
	case k == 0:
		return []hitDTO{diagnosticHit(n)}, nil
	}
	return append(shapeHits(chunks[:k], detail), diagnosticHit(n-k)), nil
}

const autoFullHits = 3

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

func toHits(chunks []parser.Chunk) []hitDTO {
	out := make([]hitDTO, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, toHit(chunk))
	}
	return out
}

func toHit(chunk parser.Chunk) hitDTO {
	hit := hitDTO{FilePath: chunk.FilePath, Lines: chunk.Content}
	if chunk.Metadata == nil {
		return hit
	}
	hit.Symbol = chunk.Metadata["name"]
	hit.Summary = chunk.Metadata["summary"]
	if r := chunk.Metadata["receiver"]; r != "" {
		hit.Receiver = r
	}
	hit.StartLine = lineNum(chunk.Metadata["start_line"])
	hit.EndLine = lineNum(chunk.Metadata["end_line"])
	return hit
}

func lineNum(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0
	}
	return n
}
