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
	return h.impactFrom(ctx, []definition{target}, in.Depth, h.newSources())
}

// dispatchersOf finds the code that makes what a handler handles. A method the
// parser marks as handling a type (handle(SomeCommand $c), __invoke(SomeEvent $e))
// is reached from the places that build a SomeCommand, since the bus, not a call,
// joins them.
func (h *tools) dispatchersOf(ctx context.Context, handler parser.Chunk, srcs *sources) []parser.Chunk {
	handled := handler.Metadata["handles"]
	if handled == "" {
		return nil
	}
	short := handled[strings.LastIndexAny(handled, `\.`)+1:]
	classes, err := h.store.ComponentsByNames(ctx, []string{short})
	if err != nil {
		return nil
	}
	var class []parser.Chunk
	for _, d := range classes[short] {
		switch d.Type {
		case "class", "struct", "type", "interface":
			if fqn := d.Metadata["fqn"]; fqn == "" || fqn == handled {
				class = append(class, d)
			}
		}
	}
	if len(class) == 0 {
		return nil
	}
	refs, err := h.store.ReferencesByNames(ctx, []string{short})
	if err != nil {
		return nil
	}
	var out []parser.Chunk
	for _, c := range refs[short] {
		if c.Metadata["handles"] == handled {
			continue // another handler of it only names the type in its signature
		}
		for _, d := range class {
			if srcs.constructs(c, d) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// handlerAt is the chunk named name in file among defs.
func handlerAt(defs []parser.Chunk, t definition) (parser.Chunk, bool) {
	for _, d := range defs {
		if d.FilePath == t.file && d.Metadata["handles"] != "" {
			return d, true
		}
	}
	return parser.Chunk{}, false
}

// callersByTarget is one level of impactFrom, kept per target: who calls each of
// them. It costs two queries however many targets there are.
func (h *tools) callersByTarget(ctx context.Context, targets []definition, srcs *sources) (map[definition][]parser.Chunk, error) {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.name
	}
	refsBy, err := h.store.ReferencesByNames(ctx, names)
	if err != nil {
		return nil, err
	}
	defsBy, err := h.store.ComponentsByNames(ctx, names)
	if err != nil {
		return nil, err
	}
	out := make(map[definition][]parser.Chunk, len(targets))
	for _, t := range targets {
		refs := refsBy[t.name]
		if t.file != "" {
			refs = attributedWith(srcs, defsBy[t.name], t, refs)
			refs = resolvable(srcs, defsBy[t.name], t, refs)
		}
		if handler, ok := handlerAt(defsBy[t.name], t); ok && t.file != "" {
			refs = append(refs, h.dispatchersOf(ctx, handler, srcs)...)
		}
		out[t] = refs
	}
	return out, nil
}

// impactFrom walks callers level by level. Each level costs two queries, one
// for the references to every name on the frontier and one for their
// definitions, however many targets there are.
func (h *tools) impactFrom(ctx context.Context, targets []definition, depth int, srcs *sources) ([]parser.Chunk, error) {
	if depth < 1 {
		depth = defaultDepth
	}
	depth = min(depth, maxDepth)
	seenDef := map[definition]bool{}
	for _, t := range targets {
		seenDef[t] = true
	}
	seenChunk := map[string]bool{}
	frontier := targets
	var out []parser.Chunk
	for level := 0; level < depth && len(frontier) > 0 && len(out) < maxImpact; level++ {
		names := make([]string, 0, len(frontier))
		attribute := false
		for _, t := range frontier {
			names = append(names, t.name)
			attribute = attribute || t.file != ""
		}
		refsBy, err := h.store.ReferencesByNames(ctx, names)
		if err != nil {
			return nil, err
		}
		var defsBy map[string][]parser.Chunk
		if attribute {
			if defsBy, err = h.store.ComponentsByNames(ctx, names); err != nil {
				return nil, err
			}
		}
		var next []definition
		for _, t := range frontier {
			refs := refsBy[t.name]
			if t.file != "" {
				refs = attributedWith(srcs, defsBy[t.name], t, refs)
				refs = resolvable(srcs, defsBy[t.name], t, refs)
				if handler, ok := handlerAt(defsBy[t.name], t); ok {
					refs = append(refs, h.dispatchersOf(ctx, handler, srcs)...)
				}
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

// resolvable drops callers whose text cannot be referring to the definition in
// t.file: a call to strings.Split is not a call to the project's Split.
func resolvable(srcs *sources, defs []parser.Chunk, t definition, refs []parser.Chunk) []parser.Chunk {
	var here []parser.Chunk
	for _, d := range defs {
		if d.FilePath == t.file {
			here = append(here, d)
		}
	}
	if len(here) == 0 {
		return refs
	}
	var out []parser.Chunk
	for _, c := range refs {
		for _, d := range here {
			if srcs.accepts(c, d, len(defs)) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// attributedWith gives a call to the definition nearest its file, unless the
// language's rules say the call cannot be to that one: a property typed as
// Cache\RateLimiter is not a call to the RateLimitedWithRedis next to it.
func attributedWith(srcs *sources, defs []parser.Chunk, t definition, refs []parser.Chunk) []parser.Chunk {
	var out []parser.Chunk
	for _, c := range refs {
		own := pathCloseness(c.FilePath, t.file)
		keep := true
		for _, d := range defs {
			if d.FilePath != t.file && pathCloseness(c.FilePath, d.FilePath) > own && srcs.accepts(c, d, len(defs)) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, c)
		}
	}
	return out
}

type graphIn struct {
	Name      string `json:"name" jsonschema:"symbol name"`
	Direction string `json:"direction,omitempty" jsonschema:"callers (default), callees or references"`
	Depth     int    `json:"depth,omitempty" jsonschema:"caller levels, 1 to 4; default 2"`
	Receiver  string `json:"receiver,omitempty" jsonschema:"receiver or class, for same-name definitions"`
	FilePath  string `json:"file_path,omitempty" jsonschema:"file of the definition, for same-name definitions"`
	Detail    string `json:"detail,omitempty" jsonschema:"brief (default): signatures; auto: top 3 with code; full"`
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
	detail := in.Detail
	switch detail {
	case "":
		detail = "brief"
	case "auto", "full", "brief":
	default:
		return nil, hitList{}, fmt.Errorf("detail must be brief, auto or full, got %q", in.Detail)
	}
	return h.finishGraph(ctx, "graph", in, start, chunks, err, detail)
}

func (h *tools) finishGraph(ctx context.Context, tool string, in any, start time.Time, chunks []parser.Chunk, err error, detail string) (*sdkmcp.CallToolResult, hitList, error) {
	if err != nil {
		h.record(tool, in, start, nil, 0)
		return nil, hitList{}, err
	}
	budget, text := h.fitBudget(chunkQuery(chunks))
	hits, err := fitHits(chunks, budget, detail)
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
