package mcp

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxDefinitions bounds how many definitions of one name onfly_xray expands.
const maxDefinitions = 5

// resolveSymbol finds the definitions a non-path target names. The target is a
// bare name ("Open"), or a name with a qualifier that picks among definitions:
// "telemetry.Open", "Summary.Saved", "Log::summary" or "internal/telemetry/telemetry.go:Open".
// When a qualifier matches nothing, every definition of the name comes back with
// a note, so the caller still gets the answer in one round trip.
func (h *tools) resolveSymbol(ctx context.Context, target string) ([]parser.Chunk, string, error) {
	defs, err := components(ctx, h.store, target)
	if err != nil {
		return nil, "", err
	}
	if len(defs) > 0 {
		return rankDefinitions(defs), "", nil
	}
	qualifier, name, isPath := splitTarget(target)
	if name == "" {
		return nil, "", nil
	}
	all, err := components(ctx, h.store, name)
	if err != nil || len(all) == 0 {
		return nil, "", err
	}
	var kept []parser.Chunk
	for _, d := range all {
		if qualifies(d, qualifier, isPath) {
			kept = append(kept, d)
		}
	}
	if len(kept) == 0 {
		return rankDefinitions(all), fmt.Sprintf("no definition of %s matches %q; showing every definition of it", name, qualifier), nil
	}
	return rankDefinitions(kept), "", nil
}

// components is GetComponents with "no such name" as an empty result.
func components(ctx context.Context, st vectorstore.Store, name string) ([]parser.Chunk, error) {
	defs, err := st.GetComponents(ctx, name)
	if errors.Is(err, vectorstore.ErrNotFound) {
		return nil, nil
	}
	return defs, err
}

// splitTarget cuts "a.b.Name", "a::Name" or "dir/file.go:Name" into the
// qualifier and the symbol name. isPath says the qualifier is a file path.
func splitTarget(target string) (qualifier, name string, isPath bool) {
	switch {
	case strings.Contains(target, "::"):
		i := strings.LastIndex(target, "::")
		return target[:i], target[i+2:], false
	case strings.Contains(target, ":"):
		i := strings.LastIndex(target, ":")
		return target[:i], target[i+1:], true
	case strings.Contains(target, "."):
		i := strings.LastIndex(target, ".")
		return target[:i], target[i+1:], false
	}
	return "", "", false
}

// qualifies reports whether a definition belongs to the qualifier: its file
// path, directory (package), receiver type or namespace.
func qualifies(d parser.Chunk, qualifier string, isPath bool) bool {
	if qualifier == "" {
		return true
	}
	file := strings.ToLower(d.FilePath)
	if isPath {
		q := strings.ToLower(strings.TrimPrefix(path.Clean(qualifier), "./"))
		return file == q || strings.HasSuffix(file, "/"+q)
	}
	labels := map[string]bool{}
	for _, seg := range strings.Split(strings.TrimSuffix(file, path.Ext(file)), "/") {
		labels[seg] = true
	}
	if r := strings.ToLower(strings.TrimLeft(d.Metadata["receiver"], "*")); r != "" {
		labels[r] = true
		for _, p := range strings.FieldsFunc(r, func(c rune) bool { return c == '.' || c == '\\' || c == ':' }) {
			labels[p] = true
		}
	}
	for _, key := range []string{"namespace", "fqn"} {
		for _, p := range strings.FieldsFunc(strings.ToLower(d.Metadata[key]), func(c rune) bool { return c == '.' || c == '\\' || c == ':' || c == '/' }) {
			labels[p] = true
		}
	}
	for _, part := range strings.FieldsFunc(strings.ToLower(qualifier), func(c rune) bool { return c == '.' || c == ':' || c == '\\' }) {
		if strings.Contains(part, "/") {
			if !strings.Contains("/"+path.Dir(file)+"/", "/"+strings.Trim(part, "/")+"/") {
				return false
			}
			continue
		}
		if !labels[part] {
			return false
		}
	}
	return true
}

// rankDefinitions puts code before tests, then orders by path and line, and
// drops repeats of one definition.
func rankDefinitions(defs []parser.Chunk) []parser.Chunk {
	seen := map[string]bool{}
	out := make([]parser.Chunk, 0, len(defs))
	for _, d := range defs {
		key := d.FilePath + "|" + d.Metadata["receiver"] + "|" + d.Metadata["start_line"]
		if !seen[key] {
			seen[key] = true
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := isTestFile(out[i].FilePath), isTestFile(out[j].FilePath)
		if ti != tj {
			return !ti
		}
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		return lineNum(out[i].Metadata["start_line"]) < lineNum(out[j].Metadata["start_line"])
	})
	return out
}

// definitionBlock is one definition with exactly what it calls and who calls it.
func (h *tools) definitionBlock(ctx context.Context, d parser.Chunk, depth int, srcs *sources) (string, int) {
	var calls, calledBy strings.Builder
	deps := map[string]bool{d.FilePath: true}
	h.writeCalls(ctx, &calls, "", []parser.Chunk{d}, srcs, deps)
	if refs, err := h.impactFrom(ctx, []definition{{d.Metadata["name"], d.FilePath}}, max(depth, 1), srcs); err == nil {
		seen := map[string]bool{}
		n := 0
		for _, r := range refs {
			key := r.FilePath + "|" + r.Metadata["name"] + "|" + r.Metadata["receiver"]
			if seen[key] || n >= maxXrayEdges || vectorstore.IsProse(r.FilePath) {
				continue
			}
			seen[key] = true
			fmt.Fprintf(&calledBy, "- %s [%s]\n", signature(r), location(r))
			deps[r.FilePath] = true
			n++
		}
	}
	return fmt.Sprintf("<definition at=%q>\n%s\n<calls>\n%s</calls>\n<called_by>\n%s</called_by>\n</definition>\n",
		location(d), signature(d), calls.String(), calledBy.String()), len(deps)
}

// xraySymbols answers a name that has several definitions with a block for
// each, so no second call is needed to see the callers of the others.
func (h *tools) xraySymbols(ctx context.Context, target, note string, defs []parser.Chunk, depth int) *sdkmcp.CallToolResult {
	shown := defs
	if len(shown) > maxDefinitions {
		shown = shown[:maxDefinitions]
	}
	srcs := h.newSources()
	var b strings.Builder
	if note != "" {
		fmt.Fprintf(&b, "<note>%s</note>\n", note)
	}
	b.WriteString("<definitions>\n")
	files := map[string]bool{}
	depFiles := 0
	for _, d := range shown {
		block, n := h.definitionBlock(ctx, d, depth, srcs)
		b.WriteString(block)
		files[d.FilePath] = true
		depFiles += n - 1
	}
	if len(defs) > len(shown) {
		fmt.Fprintf(&b, "// ... [%d more definitions omitted; qualify the name, e.g. pkg.Name] ...\n", len(defs)-len(shown))
	}
	b.WriteString("</definitions>\n")
	raw := depFiles * baselineDepTokens * bytesPerToken
	for f := range files {
		raw += h.fileSize(f)
	}
	text, n := h.rulesOnce(chunkQuery(shown))
	header := fmt.Sprintf("target=%q matches=\"%d\"", target, len(defs))
	return h.finishMacro(ctx, "onfly_xray", target, raw, header, b.String(), text, n)
}
