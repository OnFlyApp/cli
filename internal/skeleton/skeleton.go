package skeleton

import (
	"fmt"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/tokens"
)

type Span struct {
	Start, End int
	Name       string
}

// Collapse replaces the body of every span longer than minLines with a marker.
// It never rebuilds code: it copies lines of src, so docblocks, attributes and
// struct tags survive byte for byte. Everything up to and including the line
// that opens the body (a docblock, #[attributes], a signature spread over
// several lines, a brace on its own line) is kept, and so is the closing line.
// A span named focus, a span nested inside another one, and a span whose body
// brace cannot be found are left whole. Line numbers refer to src, so no
// replacement shifts another.
func Collapse(src string, spans []Span, focus string, minLines int) string {
	lines := strings.Split(src, "\n")
	sorted := append([]Span(nil), spans...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	var out []string
	next, covered := 1, 0
	for _, sp := range sorted {
		if sp.Start < 1 || sp.Start <= covered || sp.Start < next || sp.End > len(lines) || sp.End-sp.Start+1 <= minLines || (focus != "" && sp.Name == focus) {
			continue
		}
		open, ok := bodyOpen(lines, sp)
		if !ok || open >= sp.End-1 {
			continue
		}
		out = append(out, lines[next-1:open]...)
		omitted := strings.Join(lines[open:sp.End-1], "\n")
		n, _ := tokens.Count(omitted)
		out = append(out, fmt.Sprintf("%s// ... [body omitted: lines %d-%d, ~%d tokens] ...", indentOf(lines[open-1])+"\t", open+1, sp.End-1, n), lines[sp.End-1])
		next, covered = sp.End+1, sp.End
	}
	if next <= len(lines) {
		out = append(out, lines[next-1:]...)
	}
	return strings.Join(out, "\n")
}

// bodyOpen returns the 1-based line that opens the body: the first line after
// the docblock and attributes that ends in "{" or is "{" alone.
func bodyOpen(lines []string, sp Span) (int, bool) {
	for i := sp.Start; i < sp.End; i++ {
		t := strings.TrimSpace(lines[i-1])
		if t == "" || isNote(t) {
			continue
		}
		if strings.HasSuffix(t, "{") {
			return i, true
		}
	}
	return 0, false
}

func isNote(t string) bool {
	for _, p := range []string{"//", "/*", "*", "#", "@"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}
