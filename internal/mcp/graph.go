package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxCallees   = 50
	maxImpact    = 100
	defaultDepth = 2
	maxDepth     = 4
)

type impactIn struct {
	Name  string `json:"name" jsonschema:"component name from chunk metadata"`
	Depth int    `json:"depth,omitempty" jsonschema:"caller levels to follow, from 1 to 4; default 2"`
}

func (h *tools) callees(ctx context.Context, name string) ([]parser.Chunk, error) {
	root, err := h.store.GetComponentAST(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []parser.Chunk
	for _, ref := range root.Outbound {
		if len(out) >= maxCallees {
			break
		}
		c, err := h.store.GetComponentAST(ctx, ref)
		if errors.Is(err, vectorstore.ErrNotFound) {
			continue // a library or builtin name, not defined in this index
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func (h *tools) impact(ctx context.Context, name string, depth int) ([]parser.Chunk, error) {
	if depth < 1 {
		depth = defaultDepth
	}
	depth = min(depth, maxDepth)
	seenName := map[string]bool{name: true}
	seenChunk := map[string]bool{}
	frontier := []string{name}
	var out []parser.Chunk
	for level := 0; level < depth && len(frontier) > 0 && len(out) < maxImpact; level++ {
		var next []string
		for _, n := range frontier {
			refs, err := h.store.GetReferences(ctx, n)
			if err != nil {
				return nil, err
			}
			for _, c := range refs {
				key := query.ChunkKey(c)
				if seenChunk[key] {
					continue
				}
				seenChunk[key] = true
				out = append(out, c)
				if len(out) >= maxImpact {
					return out, nil
				}
				if cn := c.Metadata["name"]; cn != "" && !seenName[cn] {
					seenName[cn] = true
					next = append(next, cn)
				}
			}
		}
		frontier = next
	}
	return out, nil
}

func (h *tools) getCallees(ctx context.Context, _ *sdkmcp.CallToolRequest, in nameIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	chunks, err := h.callees(ctx, in.Name)
	if errors.Is(err, vectorstore.ErrNotFound) {
		err = fmt.Errorf("component %q not found", in.Name)
	}
	return h.finishGraph(ctx, "get_callees", in, start, chunks, err)
}

func (h *tools) getImpact(ctx context.Context, _ *sdkmcp.CallToolRequest, in impactIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	chunks, err := h.impact(ctx, in.Name, in.Depth)
	return h.finishGraph(ctx, "get_impact", in, start, chunks, err)
}

func (h *tools) finishGraph(_ context.Context, tool string, in any, start time.Time, chunks []parser.Chunk, err error) (*sdkmcp.CallToolResult, hitList, error) {
	if err != nil {
		h.record(tool, in, start, nil, 0)
		return nil, hitList{}, err
	}
	budget, text := h.fitBudget(chunkQuery(chunks))
	hits, err := fitHits(chunks, budget)
	h.record(tool, in, start, hits, len(hits))
	out := hitList{Rules: text, Hits: hits}
	if out.Hits == nil {
		out.Hits = []hitDTO{}
	}
	return contentWithRules(text, out), out, err
}
