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
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	collapseOver  = 10
	maxXrayEdges  = 15
	maxXrayNames  = 20
	maxDirEntries = 40
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
	files, err := h.store.ListFiles(ctx)
	if err != nil {
		return nil, struct{}{}, err
	}
	sort.Strings(files)
	focus := in.FocusSymbol
	var res *sdkmcp.CallToolResult
	switch {
	case contains(files, rel):
		res, err = h.xrayFile(ctx, rel, focus, in.Depth)
	case hasPrefix(files, rel+"/"):
		res, err = h.xrayDir(ctx, rel, files)
	default:
		var defs []parser.Chunk
		if defs, err = h.store.GetComponents(ctx, target); err != nil || len(defs) == 0 {
			return nil, struct{}{}, fmt.Errorf("%q is not an indexed file, directory or symbol; run onfly index", target)
		}
		if focus == "" {
			focus = defs[0].Metadata["name"]
		}
		res, err = h.xrayFile(ctx, defs[0].FilePath, focus, in.Depth)
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

func (h *tools) xrayFile(ctx context.Context, rel, focus string, depth int) (*sdkmcp.CallToolResult, error) {
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
	sk := cutToBudget(skeleton.Collapse(string(src), spans, focus, collapseOver), h.maxTokens)
	calls, calledBy := h.blastRadius(ctx, rel, chunks, depth)
	var b strings.Builder
	b.WriteString("<skeleton>\n" + sk + "\n</skeleton>\n<blast_radius>\n<calls>\n" + calls + "</calls>\n<called_by>\n" + calledBy + "</called_by>\n</blast_radius>\n")
	text, n := h.rulesOnce(chunkQuery(chunks))
	return h.finishMacro(ctx, "onfly_xray", rel, len(src), fmt.Sprintf("target=%q", rel), b.String(), text, n), nil
}

func (h *tools) blastRadius(ctx context.Context, rel string, chunks []parser.Chunk, depth int) (string, string) {
	if depth < 1 {
		depth = 1
	}
	var calls, calledBy strings.Builder
	seen := map[string]bool{}
	nOut := 0
	for _, c := range chunks {
		for _, ref := range c.Outbound {
			if seen[ref] || nOut >= maxXrayEdges {
				continue
			}
			seen[ref] = true
			defs, err := h.store.GetComponents(ctx, ref)
			if err != nil || len(defs) == 0 {
				continue
			}
			d := nearestDefinition(defs, c)
			if d.FilePath == rel {
				continue
			}
			fmt.Fprintf(&calls, "- %s [%s]\n", signature(d), location(d))
			nOut++
		}
	}
	seenIn := map[string]bool{}
	names, nIn := 0, 0
	for _, c := range chunks {
		name := c.Metadata["name"]
		if name == "" || names >= maxXrayNames || nIn >= maxXrayEdges {
			continue
		}
		switch c.Type {
		case "function", "method", "class", "struct", "interface", "trait":
		default:
			continue
		}
		names++
		refs, err := h.impact(ctx, impactIn{Name: name, Depth: depth, Receiver: c.Metadata["receiver"], FilePath: rel})
		if err != nil {
			continue
		}
		for _, r := range refs {
			key := r.FilePath + "|" + r.Metadata["name"] + "|" + r.Metadata["receiver"]
			if r.FilePath == rel || seenIn[key] || nIn >= maxXrayEdges {
				continue
			}
			seenIn[key] = true
			fmt.Fprintf(&calledBy, "- %s [%s]\n", signature(r), location(r))
			nIn++
		}
	}
	return calls.String(), calledBy.String()
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
