package mcp

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
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

type calleesIn struct {
	Name     string `json:"name" jsonschema:"component name from chunk metadata"`
	Receiver string `json:"receiver,omitempty" jsonschema:"receiver or class, to pick one of several same-name definitions"`
	FilePath string `json:"file_path,omitempty" jsonschema:"file of the definition, to pick one of several same-name definitions"`
}

type impactIn struct {
	Name     string `json:"name" jsonschema:"component name from chunk metadata"`
	Depth    int    `json:"depth,omitempty" jsonschema:"caller levels to follow, from 1 to 4; default 2"`
	Receiver string `json:"receiver,omitempty" jsonschema:"receiver or class, to pick one of several same-name definitions"`
	FilePath string `json:"file_path,omitempty" jsonschema:"file of the definition, to pick one of several same-name definitions"`
}

func pathCloseness(a, b string) int {
	if a == b {
		return 1 << 20
	}
	as, bs := strings.Split(path.Dir(a), "/"), strings.Split(path.Dir(b), "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
}

func pickDefinition(defs []parser.Chunk, receiver, file string) (parser.Chunk, bool) {
	for _, d := range defs {
		if receiver != "" && !receiverMatches(d.Metadata["receiver"], receiver) {
			continue
		}
		if file != "" && d.FilePath != file {
			continue
		}
		return d, true
	}
	return parser.Chunk{}, false
}

func nearestDefinition(defs []parser.Chunk, from parser.Chunk) parser.Chunk {
	best, bestScore := defs[0], -1
	for _, d := range defs {
		score := pathCloseness(from.FilePath, d.FilePath) * 2
		if d.Metadata["receiver"] == from.Metadata["receiver"] {
			score++
		}
		if score > bestScore {
			best, bestScore = d, score
		}
	}
	return best
}

func (h *tools) callees(ctx context.Context, in calleesIn) ([]parser.Chunk, error) {
	defs, err := h.store.GetComponents(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	root, ok := pickDefinition(defs, in.Receiver, in.FilePath)
	if !ok {
		return nil, fmt.Errorf("component %q has no definition matching receiver %q file %q", in.Name, in.Receiver, in.FilePath)
	}
	var out []parser.Chunk
	for _, ref := range root.Outbound {
		if len(out) >= maxCallees {
			break
		}
		cands, err := h.store.GetComponents(ctx, ref)
		if errors.Is(err, vectorstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, nearestDefinition(cands, root))
	}
	return out, nil
}

type definition struct{ name, file string }

func (h *tools) impact(ctx context.Context, in impactIn) ([]parser.Chunk, error) {
	depth := in.Depth
	if depth < 1 {
		depth = defaultDepth
	}
	depth = min(depth, maxDepth)
	target := definition{name: in.Name}
	if in.Receiver != "" || in.FilePath != "" {
		defs, err := h.store.GetComponents(ctx, in.Name)
		if err != nil {
			return nil, err
		}
		d, ok := pickDefinition(defs, in.Receiver, in.FilePath)
		if !ok {
			return nil, fmt.Errorf("component %q has no definition matching receiver %q file %q", in.Name, in.Receiver, in.FilePath)
		}
		target.file = d.FilePath
	}
	seenDef := map[definition]bool{target: true}
	seenChunk := map[string]bool{}
	frontier := []definition{target}
	var out []parser.Chunk
	for level := 0; level < depth && len(frontier) > 0 && len(out) < maxImpact; level++ {
		var next []definition
		for _, t := range frontier {
			refs, err := h.store.GetReferences(ctx, t.name)
			if err != nil {
				return nil, err
			}
			refs, err = h.attributed(ctx, t, refs)
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
				if cn := c.Metadata["name"]; cn != "" {
					d := definition{cn, c.FilePath}
					if !seenDef[d] {
						seenDef[d] = true
						next = append(next, d)
					}
				}
			}
		}
		frontier = next
	}
	return out, nil
}

func (h *tools) attributed(ctx context.Context, t definition, refs []parser.Chunk) ([]parser.Chunk, error) {
	if t.file == "" {
		return refs, nil
	}
	defs, err := h.store.GetComponents(ctx, t.name)
	if err != nil {
		return nil, err
	}
	var out []parser.Chunk
	for _, c := range refs {
		own := pathCloseness(c.FilePath, t.file)
		keep := true
		for _, d := range defs {
			if d.FilePath != t.file && pathCloseness(c.FilePath, d.FilePath) > own {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, c)
		}
	}
	return out, nil
}

type graphIn struct {
	Name      string `json:"name" jsonschema:"symbol name"`
	Direction string `json:"direction,omitempty" jsonschema:"callers (default), callees or references"`
	Depth     int    `json:"depth,omitempty" jsonschema:"caller levels, 1 to 4; default 2"`
	Receiver  string `json:"receiver,omitempty" jsonschema:"receiver or class, for same-name definitions"`
	FilePath  string `json:"file_path,omitempty" jsonschema:"file of the definition, for same-name definitions"`
}

func (h *tools) graph(ctx context.Context, _ *sdkmcp.CallToolRequest, in graphIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	var chunks []parser.Chunk
	var err error
	switch in.Direction {
	case "", "callers":
		chunks, err = h.impact(ctx, impactIn{Name: in.Name, Depth: in.Depth, Receiver: in.Receiver, FilePath: in.FilePath})
		if errors.Is(err, vectorstore.ErrNotFound) {
			err = fmt.Errorf("component %q not found", in.Name)
		}
	case "callees":
		chunks, err = h.callees(ctx, calleesIn{Name: in.Name, Receiver: in.Receiver, FilePath: in.FilePath})
		if errors.Is(err, vectorstore.ErrNotFound) {
			err = fmt.Errorf("component %q not found", in.Name)
		}
	case "references":
		chunks, err = h.store.GetReferences(ctx, in.Name)
	default:
		err = fmt.Errorf("direction must be callers, callees or references, got %q", in.Direction)
	}
	return h.finishGraph(ctx, "graph", in, start, chunks, err)
}

func (h *tools) finishGraph(ctx context.Context, tool string, in any, start time.Time, chunks []parser.Chunk, err error) (*sdkmcp.CallToolResult, hitList, error) {
	if err != nil {
		h.record(tool, in, start, nil, 0)
		return nil, hitList{}, err
	}
	budget, text := h.fitBudget(chunkQuery(chunks))
	hits, err := fitHits(chunks, budget, "full")
	h.markStale(ctx, hits)
	h.record(tool, in, start, hits, len(hits))
	out := hitList{Rules: text, Hits: hits}
	if out.Hits == nil {
		out.Hits = []hitDTO{}
	}
	return contentWithRules(text, out), out, err
}

// receiverMatches reports whether a stored receiver is the one asked for. A
// short class name matches the qualified one the parser stored, so
// "ModelCaller" finds "App\\Pipeline\\ModelCaller".
func receiverMatches(have, want string) bool {
	if have == want {
		return true
	}
	for _, sep := range []string{`\`, ".", "::", "/"} {
		if strings.HasSuffix(have, sep+want) {
			return true
		}
	}
	return false
}
