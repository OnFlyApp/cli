// Package lint checks the lines a change adds against the constraints in the
// project rules (forbidden_calls, forbidden_imports). The match is textual and
// per line, with no type information: a qualified entry such as fmt.Println is
// exact, a bare one such as dd matches a call that is not a method or a
// qualified name. Comments and the inside of string literals are ignored.
package lint

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/axitdev/onfly/internal/rules"
)

// Violation is one forbidden call or import on one line.
type Violation struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Match    string `json:"match"`
}

// Added is a line a change adds, with its 1-based number in the new file.
type Added struct {
	Line int
	Text string
}

// File holds the added lines of one file.
type File struct {
	Path  string
	Lines []Added
}

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ParseDiff reads the output of `git diff --unified=0` into the added lines of
// each file. Deleted files and binary files have none.
func ParseDiff(diff string) []File {
	var files []File
	var cur *File
	line := 0
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		t := sc.Text()
		switch {
		case strings.HasPrefix(t, "+++ "):
			path := strings.TrimPrefix(t, "+++ ")
			if path == "/dev/null" {
				cur = nil
				continue
			}
			path = strings.TrimPrefix(strings.Trim(path, `"`), "b/")
			files = append(files, File{Path: path})
			cur = &files[len(files)-1]
		case strings.HasPrefix(t, "--- "), strings.HasPrefix(t, "diff "), strings.HasPrefix(t, "index "):
		case strings.HasPrefix(t, "@@"):
			if m := hunkRe.FindStringSubmatch(t); m != nil {
				line, _ = strconv.Atoi(m[1])
			}
		case strings.HasPrefix(t, "+") && cur != nil:
			cur.Lines = append(cur.Lines, Added{Line: line, Text: t[1:]})
			line++
		}
	}
	return files
}

// WholeFile turns a file's content into Added lines, to lint it as a whole.
func WholeFile(path, content string) File {
	f := File{Path: path}
	for i, l := range strings.Split(content, "\n") {
		f.Lines = append(f.Lines, Added{Line: i + 1, Text: l})
	}
	return f
}

type matcher struct {
	rule  rules.Rule
	entry string
	re    *regexp.Regexp
	kind  string // "call" or "import"
}

func buildMatchers(rs []rules.Rule) []matcher {
	var out []matcher
	for _, r := range rs {
		for _, e := range r.ForbiddenCalls {
			if re := callRegexp(strings.TrimSpace(e)); re != nil {
				out = append(out, matcher{r, e, re, "call"})
			}
		}
		for _, e := range r.ForbiddenImports {
			for _, re := range importRegexps(strings.TrimSpace(e)) {
				out = append(out, matcher{r, e, re, "import"})
			}
		}
	}
	return out
}

func callRegexp(entry string) *regexp.Regexp {
	if entry == "" {
		return nil
	}
	if strings.ContainsAny(entry, ".:>") {
		parts := regexp.MustCompile(`::|->|\.`).Split(entry, -1)
		seps := regexp.MustCompile(`::|->|\.`).FindAllString(entry, -1)
		var b strings.Builder
		b.WriteString(`(?:^|[^\w$])`)
		for i, p := range parts {
			b.WriteString(regexp.QuoteMeta(p))
			if i < len(seps) {
				b.WriteString(`\s*` + regexp.QuoteMeta(seps[i]) + `\s*`)
			}
		}
		b.WriteString(`\s*\(`)
		return regexp.MustCompile(b.String())
	}
	return regexp.MustCompile(`(?:^|[^\w.>:$])` + regexp.QuoteMeta(entry) + `\s*\(`)
}

func importRegexps(entry string) []*regexp.Regexp {
	if entry == "" {
		return nil
	}
	q := regexp.QuoteMeta(strings.TrimPrefix(entry, `\`))
	return []*regexp.Regexp{
		regexp.MustCompile(`^\s*(?:import\s+)?(?:[\w.]+\s+)?"` + q + `(?:/[^"]*)?"\s*$`),
		regexp.MustCompile(`\b(?:from|require\(|import)\s*\(?\s*['"]` + q + `(?:/[^'"]*)?['"]`),
		regexp.MustCompile(`^\s*(?:use|from|import)\s+\\?` + q + `(?:[\\.:/\s;{]|$)`),
	}
}

var (
	dqString = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)
	sqString = regexp.MustCompile(`'(?:\\.|[^'\\])*'`)
)

// code removes string contents so that a message that names a forbidden call is
// not a violation.
func code(line string) string {
	return sqString.ReplaceAllString(dqString.ReplaceAllString(line, `""`), `''`)
}

func isComment(t string) bool {
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "#[") ||
		strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "--")
}

// Check returns the violations among the added lines. Rules are applied by
// their scope and ignore list; the order of the result is the order of the input.
func Check(rs []rules.Rule, files []File) []Violation {
	ms := buildMatchers(rs)
	var out []Violation
	for _, f := range files {
		for _, a := range f.Lines {
			t := strings.TrimSpace(a.Text)
			if t == "" || isComment(t) {
				continue
			}
			stripped := code(a.Text)
			for _, m := range ms {
				if !m.rule.AppliesTo(f.Path) {
					continue
				}
				subject := stripped
				if m.kind == "import" {
					subject = a.Text // an import path is a string
				}
				if loc := m.re.FindStringIndex(subject); loc != nil {
					out = append(out, Violation{
						File: f.Path, Line: a.Line, Rule: ruleName(m.rule), Severity: severity(m.rule),
						Message: message(m), Match: strings.TrimSpace(subject[loc[0]:loc[1]]),
					})
					break
				}
			}
		}
	}
	return out
}

func ruleName(r rules.Rule) string {
	if r.Name != "" {
		return r.Name
	}
	return r.Path
}

func severity(r rules.Rule) string {
	if r.Severity == "warning" || r.Severity == "warn" {
		return "warning"
	}
	return "error"
}

func message(m matcher) string {
	if m.rule.Message != "" {
		return m.rule.Message
	}
	return fmt.Sprintf("forbidden %s %s", m.kind, m.entry)
}

// OnlyPaths keeps the files at or under any of the given paths (slash-separated,
// relative to the project root).
func OnlyPaths(files []File, paths []string) []File {
	var out []File
	for _, f := range files {
		for _, p := range paths {
			p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
			if f.Path == p || strings.HasPrefix(f.Path, p+"/") {
				out = append(out, f)
				break
			}
		}
	}
	return out
}
