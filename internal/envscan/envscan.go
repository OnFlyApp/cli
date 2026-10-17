// Package envscan inventories a project's environment variables: the keys its
// .env files define, and the places in code that read them. It never reads or
// reports a value, only names.
package envscan

import (
	"bufio"
	"context"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

// FileNames are the environment files looked for in the project root.
var FileNames = []string{
	".env", ".env.example", ".env.sample", ".env.dist", ".env.template", ".env.schema",
	".env.local", ".env.development", ".env.production", ".env.staging", ".env.test", ".env.testing",
}

// Ref is one read of a variable in code.
type Ref struct {
	File string `json:"file"`
	Line int    `json:"line"`
	// Fallback is true when the call supplies a default, so the variable is optional.
	Fallback bool `json:"fallback,omitempty"`
}

// Var is one variable and everything known about it.
type Var struct {
	Name    string   `json:"name"`
	Defined []string `json:"defined,omitempty"`
	Used    []Ref    `json:"used,omitempty"`
}

// Report is the inventory.
type Report struct {
	Files   map[string]int `json:"env_files"`
	Vars    []Var          `json:"variables"`
	Missing []Var          `json:"missing"`
	Unused  []Var          `json:"unused"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseKeys lists the variable names an environment file defines. A line is
// KEY=value, optionally after export; comments and blank lines are skipped. Values
// are never kept.
func ParseKeys(content string) []string {
	var keys []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		i := strings.IndexAny(line, "=:")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		if nameRe.MatchString(key) && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

// A pattern captures the variable name in group 1; fallback, if set, says the
// call has a second argument.
type pattern struct {
	re       *regexp.Regexp
	fallback *regexp.Regexp
}

var patterns = []pattern{
	// Go
	{re: regexp.MustCompile(`\bos\.(?:Getenv|LookupEnv)\(\s*"([^"]+)"`)},
	// PHP: env('X'), env('X', default), getenv('X'), $_ENV['X'], $_SERVER['X']
	{re: regexp.MustCompile(`(?:^|[^\w>:])env\(\s*['"]([^'"]+)['"]`), fallback: regexp.MustCompile(`(?:^|[^\w>:])env\(\s*['"][^'"]+['"]\s*,`)},
	{re: regexp.MustCompile(`(?:^|[^\w.>:])getenv\(\s*['"]([^'"]+)['"]`)},
	{re: regexp.MustCompile(`\$_(?:ENV|SERVER)\[\s*['"]([A-Z][A-Z0-9_]*)['"]\s*\]`)},
	// JavaScript and TypeScript
	{re: regexp.MustCompile(`\bprocess\.env\.([A-Za-z_][A-Za-z0-9_]*)`)},
	{re: regexp.MustCompile(`\bprocess\.env\[\s*['"]([^'"]+)['"]\s*\]`)},
	{re: regexp.MustCompile(`\bimport\.meta\.env\.([A-Za-z_][A-Za-z0-9_]*)`)},
	// Python
	{re: regexp.MustCompile(`\bos\.getenv\(\s*['"]([^'"]+)['"]`), fallback: regexp.MustCompile(`\bos\.getenv\(\s*['"][^'"]+['"]\s*,`)},
	{re: regexp.MustCompile(`\bos\.environ\.get\(\s*['"]([^'"]+)['"]`), fallback: regexp.MustCompile(`\bos\.environ\.get\(\s*['"][^'"]+['"]\s*,`)},
	{re: regexp.MustCompile(`\bos\.environ\[\s*['"]([^'"]+)['"]\s*\]`)},
	// Rust
	{re: regexp.MustCompile(`\benv::var(?:_os)?\(\s*"([^"]+)"`)},
	{re: regexp.MustCompile(`\benv!\(\s*"([^"]+)"`)},
	// Ruby
	{re: regexp.MustCompile(`\bENV\[\s*['"]([^'"]+)['"]\s*\]`)},
	{re: regexp.MustCompile(`\bENV\.fetch\(\s*['"]([^'"]+)['"]`), fallback: regexp.MustCompile(`\bENV\.fetch\(\s*['"][^'"]+['"]\s*,`)},
}

// Keywords find the chunks worth scanning; each is a plain substring.
var Keywords = []string{"Getenv", "LookupEnv", "env(", "getenv", "$_ENV", "$_SERVER", "process.env", "import.meta.env", "environ", "env::var", "env!(", "ENV["}

// Hit is a read of a variable inside a chunk of code.
type Hit struct {
	Name     string
	Line     int // 1-based, relative to the chunk
	Fallback bool
}

// Find returns the variable reads in a chunk's text.
func Find(content string) []Hit {
	var out []Hit
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") {
			continue
		}
		for _, p := range patterns {
			for _, m := range p.re.FindAllStringSubmatch(line, -1) {
				if !nameRe.MatchString(m[1]) {
					continue
				}
				out = append(out, Hit{m[1], i + 1, p.fallback != nil && p.fallback.MatchString(line)})
			}
		}
	}
	return out
}

// Source gives the scan access to the index.
type Source interface {
	SearchKeyword(ctx context.Context, keyword string, limit int, workspace string) ([]parser.Chunk, error)
}

var _ Source = vectorstore.Store(nil)

// Collect builds the report for the project at root from its env files and the
// indexed chunks.
func Collect(ctx context.Context, root string, src Source) (Report, error) {
	rep := Report{Files: map[string]int{}}
	defined := map[string][]string{}
	for _, name := range FileNames {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		keys := ParseKeys(string(raw))
		rep.Files[name] = len(keys)
		for _, k := range keys {
			defined[k] = append(defined[k], name)
		}
	}
	ctx = vectorstore.WithoutProse(ctx)
	used := map[string][]Ref{}
	seenChunk := map[string]bool{}
	seenRef := map[string]bool{}
	for _, kw := range Keywords {
		chunks, err := src.SearchKeyword(ctx, kw, 2000, "")
		if err != nil {
			continue
		}
		for _, c := range chunks {
			key := c.FilePath + "|" + c.Metadata["start_line"] + "|" + c.Metadata["name"] + "|" + c.Metadata["part"]
			if seenChunk[key] || isTestPath(c.FilePath) {
				continue
			}
			seenChunk[key] = true
			start, _ := strconv.Atoi(c.Metadata["start_line"])
			for _, h := range Find(c.Content) {
				line := h.Line
				if start > 0 {
					line = start + h.Line - 1
				}
				ref := Ref{File: c.FilePath, Line: line, Fallback: h.Fallback}
				rk := h.Name + "|" + c.FilePath + "|" + strconv.Itoa(line)
				if seenRef[rk] {
					continue
				}
				seenRef[rk] = true
				used[h.Name] = append(used[h.Name], ref)
			}
		}
	}
	names := map[string]bool{}
	for n := range defined {
		names[n] = true
	}
	for n := range used {
		names[n] = true
	}
	for n := range names {
		v := Var{Name: n, Defined: defined[n], Used: used[n]}
		sort.Slice(v.Used, func(i, j int) bool {
			if v.Used[i].File != v.Used[j].File {
				return v.Used[i].File < v.Used[j].File
			}
			return v.Used[i].Line < v.Used[j].Line
		})
		rep.Vars = append(rep.Vars, v)
		switch {
		case len(v.Defined) == 0 && len(rep.Files) > 0 && !allFallback(v.Used):
			rep.Missing = append(rep.Missing, v)
		case len(v.Used) == 0:
			rep.Unused = append(rep.Unused, v)
		}
	}
	sort.Slice(rep.Vars, func(i, j int) bool { return rep.Vars[i].Name < rep.Vars[j].Name })
	sort.Slice(rep.Missing, func(i, j int) bool { return rep.Missing[i].Name < rep.Missing[j].Name })
	sort.Slice(rep.Unused, func(i, j int) bool { return rep.Unused[i].Name < rep.Unused[j].Name })
	return rep, nil
}

func allFallback(refs []Ref) bool {
	if len(refs) == 0 {
		return false
	}
	for _, r := range refs {
		if !r.Fallback {
			return false
		}
	}
	return true
}

func isTestPath(p string) bool {
	p = strings.ToLower(p)
	base := path.Base(p)
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "test.php") || strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/") || strings.Contains(p, "/__tests__/")
}
