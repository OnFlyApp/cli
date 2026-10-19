package mcp

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

const (
	maxImplementations = 15
	implCandidatePool  = 500
	// An interface of one method is satisfied by half the types of a project
	// (Close, String); past this many matches the list says nothing.
	maxSingleMethodMatches = 10
)

// implementation is a type that implements, extends or satisfies an interface.
type implementation struct {
	chunk parser.Chunk
	// how is "implements", "extends", "impl" (a Rust impl block) or "methods" (Go,
	// by method set: likely, not certain).
	how string
}

// canHaveImplementations says whether a chunk is something other chunks
// implement: an interface, a trait, or an abstract class.
func canHaveImplementations(c parser.Chunk) bool {
	switch strings.ToLower(c.Type) {
	case "interface", "trait":
		return c.Metadata["name"] != ""
	case "class":
		return c.Metadata["name"] != "" && strings.HasPrefix(strings.TrimSpace(c.Content), "abstract ")
	}
	return false
}

// shortNames lists the names in an implements, extends or trait clause:
// "A<T>, \Ns\B" gives A and B.
func shortNames(clause string) []string {
	var depth int
	var flat strings.Builder
	for _, r := range clause {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				flat.WriteRune(r)
			}
		}
	}
	var out []string
	for _, f := range strings.FieldsFunc(flat.String(), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '{' || r == '(' }) {
		f = strings.TrimLeft(f, `\`)
		if i := strings.LastIndexAny(f, `\.:`); i >= 0 {
			f = f[i+1:]
		}
		if f != "" && f != "implements" && f != "extends" && f != "for" {
			out = append(out, f)
		}
	}
	return out
}

// implementationsOf finds what implements def. Explicit relations come from the
// chunk metadata the parsers record (PHP and TypeScript implements and extends, a
// Rust impl's trait). For a Go interface, which no type declares, a type
// qualifies when it has a method of every name in the interface.
func (h *tools) implementationsOf(ctx context.Context, def parser.Chunk) []implementation {
	name := def.Metadata["name"]
	var out []implementation
	seen := map[string]bool{def.FilePath + "|" + name: true}
	add := func(c parser.Chunk, how string) {
		key := c.FilePath + "|" + c.Metadata["name"] + "|" + c.Metadata["start_line"]
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, implementation{c, how})
	}
	if cands, err := h.store.SearchKeyword(vectorstore.WithoutProse(ctx), name, implCandidatePool, ""); err == nil {
		for _, c := range cands {
			for _, field := range []string{"implements", "extends", "trait"} {
				for _, n := range shortNames(c.Metadata[field]) {
					if n == name {
						how := field
						if field == "trait" {
							how = "impl"
						}
						add(c, how)
					}
				}
			}
		}
	}
	if strings.HasSuffix(strings.ToLower(def.FilePath), ".go") && strings.EqualFold(def.Type, "interface") {
		for _, c := range h.goImplementers(ctx, def) {
			add(c, "methods")
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := isTestFile(out[i].chunk.FilePath), isTestFile(out[j].chunk.FilePath)
		if ti != tj {
			return !ti
		}
		return out[i].chunk.FilePath < out[j].chunk.FilePath
	})
	return out
}

var goMethodLine = regexp.MustCompile(`^\s*([A-Za-z_]\w*)\s*\(`)

// goInterfaceMethods reads the method names out of a Go interface's text.
// Embedded interfaces are not followed, so a type is only required to have
// the methods written here.
func goInterfaceMethods(content string) []string {
	var out []string
	seen := map[string]bool{}
	body := content
	if i := strings.Index(body, "{"); i >= 0 {
		body = body[i+1:]
	}
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") {
			continue
		}
		if m := goMethodLine.FindStringSubmatch(line); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

func (h *tools) goImplementers(ctx context.Context, iface parser.Chunk) []parser.Chunk {
	methods := goInterfaceMethods(iface.Content)
	if len(methods) == 0 {
		return nil
	}
	by, err := h.store.ComponentsByNames(ctx, methods)
	if err != nil {
		return nil
	}
	type typeKey struct{ dir, recv string }
	have := map[typeKey]map[string]parser.Chunk{}
	for name, defs := range by {
		for _, d := range defs {
			recv := strings.TrimLeft(d.Metadata["receiver"], "*")
			if recv == "" || !strings.EqualFold(d.Type, "method") {
				continue
			}
			k := typeKey{dirOf(d.FilePath), recv}
			if have[k] == nil {
				have[k] = map[string]parser.Chunk{}
			}
			have[k][name] = d
		}
	}
	var found []parser.Chunk
	for k, ms := range have {
		if len(ms) < len(methods) || k.recv == iface.Metadata["name"] {
			continue
		}
		// Show the type's declaration when there is one, else its first method.
		show := firstMethod(ms)
		if decls, err := h.store.GetComponents(ctx, k.recv); err == nil {
			for _, d := range decls {
				if dirOf(d.FilePath) == k.dir && d.Metadata["receiver"] == "" && !strings.EqualFold(d.Type, "method") {
					show = d
					break
				}
			}
		}
		found = append(found, show)
	}
	if len(methods) == 1 && len(found) > maxSingleMethodMatches {
		return nil
	}
	return found
}

func firstMethod(ms map[string]parser.Chunk) parser.Chunk {
	names := make([]string, 0, len(ms))
	for n := range ms {
		names = append(names, n)
	}
	sort.Strings(names)
	return ms[names[0]]
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}

// implementationsBlock renders what implements each of the chunks that can have
// implementations, at most three of them.
func (h *tools) implementationsBlock(ctx context.Context, chunks []parser.Chunk) string {
	var b strings.Builder
	n := 0
	for _, c := range chunks {
		if !canHaveImplementations(c) || n >= 3 {
			continue
		}
		impls := h.implementationsOf(ctx, c)
		if len(impls) == 0 {
			continue
		}
		n++
		fmt.Fprintf(&b, "implementations of %s:\n", c.Metadata["name"])
		for i, im := range impls {
			if i == maxImplementations {
				fmt.Fprintf(&b, "// ... [%d more] ...\n", len(impls)-i)
				break
			}
			note := im.how
			if im.how == "methods" {
				note = "likely: has all its methods"
			}
			fmt.Fprintf(&b, "- %s [%s] (%s)\n", signature(im.chunk), location(im.chunk), note)
		}
	}
	return b.String()
}
