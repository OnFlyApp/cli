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

// Collapse replaces the body of every span longer than minLines with a marker,
// keeping its first and last line. A span named focus, and spans nested inside
// another span, are left alone.
func Collapse(src string, spans []Span, focus string, minLines int) string {
	lines := strings.Split(src, "\n")
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	var out []string
	next := 1
	covered := 0
	for _, sp := range spans {
		if sp.Start < next || sp.Start <= covered || sp.End > len(lines) || sp.End-sp.Start+1 <= minLines || (focus != "" && sp.Name == focus) {
			continue
		}
		out = append(out, lines[next-1:sp.Start]...)
		body := strings.Join(lines[sp.Start:sp.End-1], "\n")
		n, _ := tokens.Count(body)
		indent := lines[sp.Start-1][:len(lines[sp.Start-1])-len(strings.TrimLeft(lines[sp.Start-1], " \t"))] + "\t"
		out = append(out, fmt.Sprintf("%s// ... [body omitted: lines %d-%d, ~%d tokens] ...", indent, sp.Start+1, sp.End-1, n), lines[sp.End-1])
		next = sp.End + 1
		covered = sp.End
	}
	if next <= len(lines) {
		out = append(out, lines[next-1:]...)
	}
	return strings.Join(out, "\n")
}
