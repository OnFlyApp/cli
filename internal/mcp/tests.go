package mcp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/ignore"
	"github.com/bmatcuk/doublestar/v4"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxTestHits     = 20
	maxTestFiles    = 2000
	maxTestFileSize = 1 << 20
)

type testsIn struct {
	Name string `json:"name" jsonschema:"symbol to look for, matched as a whole word"`
	Path string `json:"path,omitempty" jsonschema:"only scan test files matching this glob, relative to the project root"`
}

type testHit struct {
	File      string `json:"file_path"`
	Line      int    `json:"line"`
	Enclosing string `json:"enclosing,omitempty"`
	Snippet   string `json:"snippet"`
}

type testList struct {
	Tests     []testHit `json:"tests"`
	Truncated bool      `json:"truncated,omitempty"`
}

var (
	testBaseRe = regexp.MustCompile(`(^test_.*\.py$)|(_test\.(go|py|rb|rs)$)|(\.(test|spec)\.(js|jsx|ts|tsx|mjs|cjs)$)|(_spec\.rb$)|(Tests?\.(php|java|kt|cs|swift)$)`)
	testDirs   = map[string]bool{"tests": true, "test": true, "__tests__": true, "spec": true}
	// testDeclRe finds the line that opens a test, so a hit can name its test.
	testDeclRe = regexp.MustCompile(`^\s*(func\s+Test|func\s+test|def\s+test_|fn\s+test_|#\[test\]|\[Fact\]|\[Test\]|@Test|public\s+function\s+test|(it|test|describe)\s*\()`)
)

// isTestFile reports whether a slash-separated project path looks like a test
// file by name or by living in a test directory. Nothing is parsed, so it
// works for any language whose convention is listed.
func isTestFile(rel string) bool {
	if testBaseRe.MatchString(path.Base(rel)) {
		return true
	}
	dir := path.Dir(rel)
	if dir == "." {
		return false
	}
	for _, part := range strings.Split(dir, "/") {
		if testDirs[part] {
			return true
		}
	}
	return false
}

// findTests lists lines of test files that mention name as a whole word, at
// most limit of them. truncated is true when the hit or file cap stopped it.
func findTests(root string, filter *ignore.Filter, name, glob string, limit int) ([]testHit, bool, error) {
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(name) + `\b`)
	if err != nil {
		return nil, false, err
	}
	hits := []testHit{}
	files, truncated := 0, false
	errStop := fmt.Errorf("stop")
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if filter.Skip(rel, true) {
				return fs.SkipDir
			}
			return nil
		}
		if !isTestFile(rel) || filter.Skip(rel, false) {
			return nil
		}
		if glob != "" {
			if ok, _ := doublestar.PathMatch(glob, rel); !ok {
				return nil
			}
		}
		if files++; files > maxTestFiles {
			truncated = true
			return errStop
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > maxTestFileSize || !info.Mode().IsRegular() {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		enclosing := ""
		for i, line := range strings.Split(string(raw), "\n") {
			if testDeclRe.MatchString(line) {
				enclosing = strings.TrimSpace(line)
			}
			if !re.MatchString(line) {
				continue
			}
			if len(hits) >= limit {
				truncated = true
				return errStop
			}
			hits = append(hits, testHit{File: rel, Line: i + 1, Enclosing: enclosing, Snippet: clip(strings.TrimSpace(line), 200)})
		}
		return nil
	})
	if err != nil && err != errStop {
		return nil, false, err
	}
	return hits, truncated, nil
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (h *tools) getTests(_ context.Context, _ *sdkmcp.CallToolRequest, in testsIn) (*sdkmcp.CallToolResult, testList, error) {
	start := time.Now()
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, testList{}, fmt.Errorf("name is required")
	}
	if in.Path != "" && !doublestar.ValidatePattern(in.Path) {
		return nil, testList{}, fmt.Errorf("invalid path pattern %q", in.Path)
	}
	hits, truncated, err := findTests(h.root, h.filter, name, in.Path, maxTestHits)
	if err != nil {
		return nil, testList{}, err
	}
	out := testList{Tests: hits, Truncated: truncated}
	h.record("get_tests", in, start, out, len(hits))
	return nil, out, nil
}
