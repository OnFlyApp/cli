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

// bodyOpen returns the 1-based line holding the "{" that opens the body. It
// reads the signature as code: braces inside (...) or [...], inside string
// literals and comments, and braces of a type literal (interface{}, struct{...},
// {}) are not the body. That keeps "meta map[string]interface{}", an attribute
// like #[Route('/items/{id}')] and a default of '{x}' inside the signature.
func bodyOpen(lines []string, sp Span) (int, bool) {
	var sc scanner
	for ln := sp.Start; ln < sp.End; ln++ {
		if sc.line(lines[ln-1]) {
			return ln, true
		}
	}
	return 0, false
}

type scanner struct {
	paren, bracket int
	skip           int // depth inside a type literal's braces
	block          bool
	quote          byte
	word           string
	inWord         bool
}

func (sc *scanner) line(line string) bool {
	if sc.quote != '`' {
		sc.quote = 0
	}
	sc.inWord = false
	for i := 0; i < len(line); i++ {
		c := line[i]
		var next byte
		if i+1 < len(line) {
			next = line[i+1]
		}
		switch {
		case sc.block:
			if c == '*' && next == '/' {
				sc.block = false
				i++
			}
		case sc.quote != 0:
			if c == '\\' && sc.quote != '`' {
				i++
			} else if c == sc.quote {
				sc.quote = 0
			}
		case c == '/' && next == '/', c == '#' && next != '[':
			return false
		case c == '/' && next == '*':
			sc.block = true
			i++
		case c == '"' || c == '\'' || c == '`':
			sc.quote, sc.word, sc.inWord = c, "", false
		case sc.skip > 0 && c == '{':
			sc.skip++
		case sc.skip > 0 && c == '}':
			sc.skip--
		case c == '(':
			sc.paren++
		case c == ')':
			sc.paren = max(sc.paren-1, 0)
		case c == '[':
			sc.bracket++
		case c == ']':
			sc.bracket = max(sc.bracket-1, 0)
		case c == '{' && sc.paren == 0 && sc.bracket == 0:
			if sc.word == "interface" || sc.word == "struct" || strings.HasPrefix(strings.TrimSpace(line[i+1:]), "}") {
				sc.skip = 1
				continue
			}
			return true
		case isIdent(c):
			if !sc.inWord {
				sc.word = ""
			}
			sc.word += string(c)
			sc.inWord = true
		case c == ' ' || c == '\t':
			sc.inWord = false
		default:
			sc.word, sc.inWord = "", false
		}
	}
	return false
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// Signature is the first line of a chunk that is code: the doc comment and the
// attributes above a declaration are skipped.
func Signature(content string) string {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || isNote(t) {
			continue
		}
		return t
	}
	first, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
	return first
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
