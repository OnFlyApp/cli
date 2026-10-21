package mcp

import (
	"context"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/rules"
	"github.com/axitdev/onfly/internal/skeleton"
	"github.com/axitdev/onfly/internal/tokens"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// smallFile is the length in lines under which a file is shown whole.
var smallFile = 120

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
	// "file.go:Symbol" syncs the file part, not the whole string.
	warmRel := rel
	if q, _, isPath := splitTarget(target); isPath && q != "" {
		warmRel = path.Clean(strings.TrimPrefix(requestPath(h.root, q), "./"))
	}
	if err := h.warm(ctx, warmRel); err != nil {
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
		res, err = h.xrayFile(ctx, rel, focus, in.Depth, "", "")
	case hasPrefix(files, rel+"/"):
		res, err = h.xrayDir(ctx, rel, files)
	default:
		defs, note, rerr := h.resolveSymbol(ctx, target)
		if rerr != nil || len(defs) == 0 {
			// "file.go:Missing": the file is real, so give it, and say what it has.
			if file, ok := h.fileOfTarget(target, files); ok {
				if focus == "" {
					_, focus, _ = splitTarget(target)
				}
				res, err = h.xrayFile(ctx, file, focus, in.Depth, "", "")
				return res, struct{}{}, err
			}
			return nil, struct{}{}, fmt.Errorf("%q is not a file, directory or symbol in this project", target)
		}
		if !sameFile(defs) {
			return h.xraySymbols(ctx, target, note, defs, in.Depth), struct{}{}, nil
		}
		// One file holds every definition of the name: the skeleton of that file
		// with all of them expanded.
		if focus == "" {
			focus = defs[0].Metadata["name"]
		}
		srcs := h.newSources()
		var blocks strings.Builder
		for i, d := range defs {
			if i == maxDefinitions {
				break
			}
			block, _ := h.definitionBlock(ctx, d, in.Depth, srcs, false)
			blocks.WriteString(block)
		}
		res, err = h.xrayFile(ctx, defs[0].FilePath, focus, in.Depth, blocks.String(), note)
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

func (h *tools) xrayFile(ctx context.Context, rel, focus string, depth int, symbols, note string) (*sdkmcp.CallToolResult, error) {
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
	over := collapseOver
	if strings.Count(string(src), "\n")+1 <= smallFile {
		over = math.MaxInt // one handler or calculator: its body is the answer
	}
	// The body asked for comes first and apart: in a long file the skeleton is cut
	// to the budget, and a body expanded inside it can fall past the cut.
	sk, focusText := cutToBudget(skeleton.CollapseNumbered(string(src), spans, "", over), h.maxTokens), ""
	if focus != "" {
		names, closest := closestSymbols(spans, focus)
		switch {
		case len(names) == 0 && symbols == "":
			note = joinNotes(note, h.missingSymbolNote(ctx, rel, rel+":"+focus))
		case closest:
			note = joinNotes(note, fmt.Sprintf("no symbol %q in this file; expanded the closest by name: %s", focus, strings.Join(names, ", ")))
		}
		if over != math.MaxInt {
			focusText = h.focusSections(string(src), spans, chunks, names)
		}
	}
	srcs := h.newSources()
	srcs.text[rel] = string(src)
	calls, calledBy, depFiles := h.blastRadius(ctx, rel, chunks, depth, srcs)
	var b strings.Builder
	if note != "" {
		b.WriteString("> " + note + "\n")
	}
	b.WriteString(focusText)
	b.WriteString("## skeleton\n" + sk + "\n")
	if calls != "" {
		b.WriteString("## calls\n" + calls)
	}
	if calledBy != "" {
		b.WriteString("## called_by\n" + calledBy)
	}
	if symbols != "" {
		b.WriteString("## definitions\n" + symbols)
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
	var shown []parser.Chunk
	for _, r := range refs {
		key := r.FilePath + "|" + r.Metadata["name"] + "|" + r.Metadata["receiver"]
		if r.FilePath == rel || seen[key] || len(shown) >= maxXrayEdges || vectorstore.IsProse(r.FilePath) {
			continue
		}
		seen[key] = true
		shown = append(shown, r)
		deps[r.FilePath] = true
	}
	var ups map[definition][]parser.Chunk
	if depth <= 1 {
		ups = h.callersOfCallers(ctx, shown, rel, srcs)
	}
	for _, r := range shown {
		fmt.Fprintf(&calledBy, "- %s [%s]%s\n", signature(r), location(r), upPreview(ups[definition{r.Metadata["name"], r.FilePath}]))
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
				if srcs.accepts(caller, d, len(cands)) {
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
		// Declarations come first so the cap of 8 names never hides a class
		// behind its methods; loose statements are not a name worth listing.
		n, typ := r.Chunk.Metadata["name"], strings.ToLower(r.Chunk.Type)
		switch {
		case n == "" || typ == "statement":
		case typ == "class" || typ == "interface" || typ == "trait" || typ == "struct" || typ == "enum":
			byFile[r.Chunk.FilePath] = append([]string{n}, byFile[r.Chunk.FilePath]...)
		default:
			byFile[r.Chunk.FilePath] = append(byFile[r.Chunk.FilePath], n)
		}
	}
	raw := 0
	var b strings.Builder
	b.WriteString("## skeleton\n")
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

func sameFile(defs []parser.Chunk) bool {
	for _, d := range defs[1:] {
		if d.FilePath != defs[0].FilePath {
			return false
		}
	}
	return true
}

// fileOfTarget reads "path/to/file.go:Name" and finds the one indexed file the
// path part names, by full path or by its end ("eviction_manager.go").
func (h *tools) fileOfTarget(target string, files []string) (string, bool) {
	q, _, isPath := splitTarget(target)
	if !isPath || q == "" {
		return "", false
	}
	q = path.Clean(strings.TrimPrefix(filepath.ToSlash(q), "./"))
	var found []string
	for _, f := range files {
		if f == q || strings.HasSuffix(f, "/"+q) {
			found = append(found, f)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// missingSymbolNote says that a symbol asked for is not in the file, and lists
// the ones that are, so the next call can be right.
func (h *tools) missingSymbolNote(ctx context.Context, file, target string) string {
	_, name, _ := splitTarget(target)
	rows, err := h.store.ChunksByFiles(ctx, []string{file})
	if err != nil {
		return fmt.Sprintf("no symbol %q in this file", name)
	}
	var names []string
	seen := map[string]bool{}
	for _, r := range rows {
		n := r.Chunk.Metadata["name"]
		if n == "" || seen[n] || (r.Chunk.Type != "function" && r.Chunk.Type != "method") {
			continue
		}
		seen[n] = true
		if len(names) < 30 {
			names = append(names, n)
		}
	}
	return fmt.Sprintf("no symbol %q in this file (showing the whole skeleton). Functions and methods here: %s", name, strings.Join(names, ", "))
}

// focusBody is the numbered source of every function called name, cut to the
// token budget.
func focusBody(src string, spans []skeleton.Span, name string, maxTokens int) string {
	lines := strings.Split(src, "\n")
	var b strings.Builder
	for _, sp := range spans {
		if sp.Name != name || sp.Start < 1 || sp.End > len(lines) {
			continue
		}
		for n := sp.Start; n <= sp.End; n++ {
			fmt.Fprintf(&b, "%d| %s\n", n, lines[n-1])
		}
	}
	return strings.TrimRight(cutToBudget(b.String(), maxTokens), "\n")
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

const maxClosest = 3

// closestSymbols finds the functions of a file that a name stands for: the name
// itself, else the ones that differ only in case, else those that begin with
// it, else those that contain it ("end" for endCycle). The second result is true
// when the name was not found as it is.
func closestSymbols(spans []skeleton.Span, name string) ([]string, bool) {
	has := func(match func(string) bool) []string {
		var out []string
		seen := map[string]bool{}
		for _, sp := range spans {
			if sp.Name != "" && !seen[sp.Name] && match(sp.Name) {
				seen[sp.Name] = true
				out = append(out, sp.Name)
			}
		}
		if len(out) > maxClosest {
			out = out[:maxClosest]
		}
		return out
	}
	low := strings.ToLower(name)
	if exact := has(func(n string) bool { return n == name }); len(exact) > 0 {
		return exact, false
	}
	for _, match := range []func(string) bool{
		func(n string) bool { return strings.ToLower(n) == low },
		func(n string) bool { return strings.HasPrefix(strings.ToLower(n), low) },
		func(n string) bool { return strings.Contains(strings.ToLower(n), low) },
	} {
		if got := has(match); len(got) > 0 {
			return got, true
		}
	}
	return nil, false
}

const maxHelpers = 2

// focusSections is the body of each function asked for, each under its name,
// then, in the room that is left, the longest helpers of the same file that
// those functions call: they are collapsed in the skeleton, and the next call
// would be for them.
func (h *tools) focusSections(src string, spans []skeleton.Span, chunks []parser.Chunk, names []string) string {
	var b strings.Builder
	used := 0
	for _, name := range names {
		body := focusBody(src, spans, name, h.maxTokens)
		if body == "" {
			continue
		}
		b.WriteString("## " + name + "\n" + body + "\n")
		n, _ := tokens.Count(body)
		used += n
	}
	for _, helper := range sameFileCallees(spans, chunks, names) {
		body := focusBody(src, spans, helper, h.maxTokens)
		n, err := tokens.Count(body)
		if body == "" || err != nil || used+n > h.maxTokens {
			continue
		}
		b.WriteString("## " + helper + " (called by the above, same file)\n" + body + "\n")
		used += n
	}
	return b.String()
}

// sameFileCallees lists the functions of the file that the named ones call and
// that the skeleton collapses, in the order the first call appears.
func sameFileCallees(spans []skeleton.Span, chunks []parser.Chunk, names []string) []string {
	long := map[string]bool{}
	for _, sp := range spans {
		if sp.End-sp.Start+1 > collapseOver {
			long[sp.Name] = true
		}
	}
	asked := map[string]bool{}
	for _, n := range names {
		asked[n] = true
	}
	type callee struct {
		name string
		at   int
	}
	var found []callee
	seen := map[string]bool{}
	for _, c := range chunks {
		if !asked[c.Metadata["name"]] || (c.Type != "function" && c.Type != "method") {
			continue
		}
		for _, ref := range c.Outbound {
			if !long[ref] || asked[ref] || seen[ref] {
				continue
			}
			seen[ref] = true
			at := strings.Index(c.Content, ref)
			found = append(found, callee{ref, at})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].at < found[j].at })
	var out []string
	for _, f := range found {
		if len(out) == maxHelpers {
			break
		}
		out = append(out, f.name)
	}
	return out
}

const (
	maxUpCallers = 3 // a caller with more callers than this is an interface, not an entrypoint
	maxUpNames   = 3
)

// callersOfCallers finds, in two queries, who calls each of the callers shown, so
// that the entrypoint two hops up can be named on the caller's line. Test
// files and the file being read do not count.
func (h *tools) callersOfCallers(ctx context.Context, callers []parser.Chunk, rel string, srcs *sources) map[definition][]parser.Chunk {
	var targets []definition
	seen := map[definition]bool{}
	for _, c := range callers {
		d := definition{c.Metadata["name"], c.FilePath}
		if d.name != "" && !seen[d] {
			seen[d] = true
			targets = append(targets, d)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	by, err := h.callersByTarget(ctx, targets, srcs)
	if err != nil {
		return nil
	}
	for t, cs := range by {
		var keep []parser.Chunk
		for _, c := range cs {
			if c.FilePath != rel && c.FilePath != t.file && !isTestFile(c.FilePath) && !vectorstore.IsProse(c.FilePath) {
				keep = append(keep, c)
			}
		}
		by[t] = keep
	}
	return by
}

// upPreview is " <- Handler::handle (Handler.php)" for a caller with one to
// three callers of its own, and nothing for any other.
func upPreview(ups []parser.Chunk) string {
	if len(ups) == 0 || len(ups) > maxUpCallers {
		return ""
	}
	var parts []string
	for _, u := range ups[:min(len(ups), maxUpNames)] {
		name := u.Metadata["name"]
		if r := u.Metadata["receiver"]; r != "" {
			if i := strings.LastIndexAny(r, `\.`); i >= 0 {
				r = r[i+1:]
			}
			name = r + "::" + name
		}
		parts = append(parts, name+" ("+path.Base(u.FilePath)+")")
	}
	return " <- " + strings.Join(parts, ", ")
}
