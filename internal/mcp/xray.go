package mcp

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/rules"
	"github.com/axitdev/onfly/internal/skeleton"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	collapseOver  = 10
	maxXrayEdges  = 15
	maxXrayNames  = 20
	maxXrayRefs   = 100
	maxDirEntries = 40

	baselineDepTokens = 150
	bytesPerToken     = 4
)

type xrayIn struct {
	Target      string `json:"target" jsonschema:"file path, directory path or symbol name"`
	FocusSymbol string `json:"focus_symbol,omitempty" jsonschema:"method or function to keep fully expanded"`
	Depth       int    `json:"depth,omitempty" jsonschema:"caller levels to follow, 1 to 4; default 1"`
}

func (h *tools) xray(ctx context.Context, req *sdkmcp.CallToolRequest, in xrayIn) (*sdkmcp.CallToolResult, struct{}, error) {
	start := time.Now()
	res, out, err := h.runXray(ctx, req, in)
	h.record("onfly_xray", in, start, resultText(res), 0)
	return res, out, err
}

func (h *tools) runXray(ctx context.Context, _ *sdkmcp.CallToolRequest, in xrayIn) (*sdkmcp.CallToolResult, struct{}, error) {
	target := strings.TrimSpace(in.Target)
	if target == "" {
		return nil, struct{}{}, fmt.Errorf("target is required")
	}
	rel := path.Clean(strings.TrimPrefix(requestPath(h.root, target), "./"))
	if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return nil, struct{}{}, fmt.Errorf("target must be inside the project")
	}
	if err := h.warm(ctx, rel); err != nil {
		return nil, struct{}{}, err
	}
	files, err := h.store.ListFiles(ctx)
	if err != nil {
		return nil, struct{}{}, err
	}
	sort.Strings(files)
	focus := in.FocusSymbol
	var res *sdkmcp.CallToolResult
	switch {
	case contains(files, rel):
		res, err = h.xrayFile(ctx, rel, focus, in.Depth, "")
	case hasPrefix(files, rel+"/"):
		res, err = h.xrayDir(ctx, rel, files)
	default:
		defs, note, rerr := h.resolveSymbol(ctx, target)
		if rerr != nil || len(defs) == 0 {
			return nil, struct{}{}, fmt.Errorf("%q is not a file, directory or symbol in this project", target)
		}
		if len(defs) > 1 {
			return h.xraySymbols(ctx, target, note, defs, in.Depth), struct{}{}, nil
		}
		if focus == "" {
			focus = defs[0].Metadata["name"]
		}
		block, _ := h.definitionBlock(ctx, defs[0], in.Depth, h.newSources())
		res, err = h.xrayFile(ctx, defs[0].FilePath, focus, in.Depth, block)
	}
	return res, struct{}{}, err
}

func contains(files []string, rel string) bool {
	i := sort.SearchStrings(files, rel)
	return i < len(files) && files[i] == rel
}

func hasPrefix(files []string, prefix string) bool {
	i := sort.SearchStrings(files, prefix)
	return i < len(files) && strings.HasPrefix(files[i], prefix)
}

func (h *tools) xrayFile(ctx context.Context, rel, focus string, depth int, symbols string) (*sdkmcp.CallToolResult, error) {
	rows, err := h.store.ChunksByFiles(ctx, []string{rel})
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(filepath.Join(h.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	var spans []skeleton.Span
	chunks := make([]parser.Chunk, 0, len(rows))
	for _, r := range rows {
		c := r.Chunk
		chunks = append(chunks, c)
		if c.Type != "function" && c.Type != "method" {
			continue
		}
		s, e := lineNum(c.Metadata["start_line"]), lineNum(c.Metadata["end_line"])
		if s > 0 && e > s {
			spans = append(spans, skeleton.Span{Start: s, End: e, Name: c.Metadata["name"]})
		}
	}
	sk := cutToBudget(skeleton.CollapseNumbered(string(src), spans, focus, collapseOver), h.maxTokens)
	srcs := h.newSources()
	srcs.text[rel] = string(src)
	calls, calledBy, depFiles := h.blastRadius(ctx, rel, chunks, depth, srcs)
	var b strings.Builder
	b.WriteString("<skeleton>\n" + sk + "\n</skeleton>\n<blast_radius>\n<calls>\n" + calls + "</calls>\n<called_by>\n" + calledBy + "</called_by>\n</blast_radius>\n")
	if symbols != "" {
		b.WriteString("<definitions>\n" + symbols + "</definitions>\n")
	}
	if symbols == "" {
		b.WriteString(h.implementationsBlock(ctx, chunks))
	}
	text, n := h.rulesOnce(chunkQuery(chunks))
	// Without onfly the assistant reads the file and spends about 150 tokens of
	// grep and read context on each file it depends on or that depends on it.
	raw := len(src) + depFiles*baselineDepTokens*bytesPerToken
	return h.finishMacro(ctx, "onfly_xray", rel, raw, fmt.Sprintf("target=%q", rel), b.String(), text, n), nil
}

// blastRadius needs a fixed number of queries whatever the size of the file:
// one for the definitions of everything it references and, per caller level,
// two for the references to its symbols.
func (h *tools) blastRadius(ctx context.Context, rel string, chunks []parser.Chunk, depth int, srcs *sources) (string, string, int) {
	var calls, calledBy strings.Builder
	deps := map[string]bool{}
	h.writeCalls(ctx, &calls, rel, chunks, srcs, deps)
	var targets []definition
	seenName := map[string]bool{}
	for _, c := range chunks {
		name := c.Metadata["name"]
		if name == "" || seenName[name] || len(targets) >= maxXrayNames {
			continue
		}
		switch c.Type {
		case "function", "method", "class", "struct", "interface", "trait":
			seenName[name] = true
			targets = append(targets, definition{name, rel})
		}
	}
	refs, err := h.impactFrom(ctx, targets, max(depth, 1), srcs)
	if err != nil {
		return calls.String(), "", len(deps)
	}
	seen := map[string]bool{}
	n := 0
	for _, r := range refs {
		key := r.FilePath + "|" + r.Metadata["name"] + "|" + r.Metadata["receiver"]
		if r.FilePath == rel || seen[key] || n >= maxXrayEdges || vectorstore.IsProse(r.FilePath) {
			continue
		}
		seen[key] = true
		fmt.Fprintf(&calledBy, "- %s [%s]\n", signature(r), location(r))
		deps[r.FilePath] = true
		n++
	}
	return calls.String(), calledBy.String(), len(deps)
}

func (h *tools) writeCalls(ctx context.Context, w *strings.Builder, rel string, chunks []parser.Chunk, srcs *sources, deps map[string]bool) {
	defs, src := h.referencedDefinitions(ctx, chunks, maxXrayRefs, srcs)
	n := 0
	for _, ref := range src.order {
		cands := defs[ref]
		if len(cands) == 0 || n >= maxXrayEdges {
			continue
		}
		d := nearestDefinition(cands, src.by[ref][0])
		if d.FilePath == rel || vectorstore.IsProse(d.FilePath) {
			continue
		}
		fmt.Fprintf(w, "- %s [%s]\n", signature(d), location(d))
		deps[d.FilePath] = true
		n++
	}
}

type refSources struct {
	order []string
	by    map[string][]parser.Chunk
}

// referencedDefinitions resolves the names the chunks use with one query, then
// keeps only the definitions a calling chunk can actually be referring to. It
// also returns, for each name, the chunks that use it.
func (h *tools) referencedDefinitions(ctx context.Context, chunks []parser.Chunk, limit int, srcs *sources) (map[string][]parser.Chunk, refSources) {
	src := refSources{by: map[string][]parser.Chunk{}}
	for _, c := range chunks {
		for _, ref := range c.Outbound {
			if builtins[ref] {
				continue
			}
			if _, seen := src.by[ref]; !seen {
				if len(src.order) >= limit {
					continue
				}
				src.order = append(src.order, ref)
			}
			src.by[ref] = append(src.by[ref], c)
		}
	}
	all, err := h.store.ComponentsByNames(ctx, src.order)
	if err != nil {
		return nil, refSources{by: map[string][]parser.Chunk{}}
	}
	defs := make(map[string][]parser.Chunk, len(all))
	for ref, cands := range all {
		for _, d := range cands {
			for _, caller := range src.by[ref] {
				if srcs.accepts(caller, d) {
					defs[ref] = append(defs[ref], d)
					break
				}
			}
		}
	}
	return defs, src
}

func (h *tools) xrayDir(ctx context.Context, rel string, files []string) (*sdkmcp.CallToolResult, error) {
	var under []string
	for _, f := range files {
		if strings.HasPrefix(f, rel+"/") {
			under = append(under, f)
		}
	}
	shown := under
	if len(shown) > maxDirEntries {
		shown = shown[:maxDirEntries]
	}
	rows, err := h.store.ChunksByFiles(ctx, shown)
	if err != nil {
		return nil, err
	}
	byFile := map[string][]string{}
	var all []parser.Chunk
	for _, r := range rows {
		all = append(all, r.Chunk)
		if n := r.Chunk.Metadata["name"]; n != "" {
			byFile[r.Chunk.FilePath] = append(byFile[r.Chunk.FilePath], n)
		}
	}
	raw := 0
	var b strings.Builder
	b.WriteString("<skeleton>\n")
	for _, f := range shown {
		raw += h.fileSize(f)
		names := byFile[f]
		more := ""
		if len(names) > 8 {
			more, names = fmt.Sprintf(" (+%d)", len(names)-8), names[:8]
		}
		fmt.Fprintf(&b, "%s: %s%s\n", f, strings.Join(names, ", "), more)
	}
	if len(under) > len(shown) {
		fmt.Fprintf(&b, "// ... [%d more files omitted] ...\n", len(under)-len(shown))
	}
	b.WriteString("</skeleton>\n")
	text, n := h.rulesOnce(rules.Query{Targets: chunkQuery(all).Targets})
	return h.finishMacro(ctx, "onfly_xray", rel, raw, fmt.Sprintf("target=%q", rel+"/"), b.String(), text, n), nil
}

// builtins are Go's predeclared functions; a method that shares one of these
// names (func (l *Log) append) is not what the call refers to.
var builtins = map[string]bool{
	"append": true, "cap": true, "clear": true, "close": true, "complex": true, "copy": true,
	"delete": true, "imag": true, "len": true, "make": true, "max": true, "min": true,
	"new": true, "panic": true, "print": true, "println": true, "real": true, "recover": true,
}
