package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxFailures     = 8
	maxMessageLines = 25
)

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// A path with a known source extension and a line number: x_test.go:12,
	// /abs/app/Foo.php:40, src/a.ts:7:3, File "x.py", line 9 is handled apart.
	frameRe  = regexp.MustCompile(`((?:[A-Za-z]:)?[\w@./\\+-]+\.(?:go|php|py|js|jsx|ts|tsx|mjs|rb|rs|java|kt|cs|swift)):(\d+)`)
	pyFrame  = regexp.MustCompile(`File "([^"]+\.py)", line (\d+)`)
	vendorRe = regexp.MustCompile(`(^|/)(vendor|node_modules|site-packages|\.venv|dist-packages)/|/usr/local/go/|/usr/lib/go|/opt/homebrew/(opt/go|Cellar/go)`)
)

func parse(res *Result, words []string, out []byte, root string) {
	if len(words) >= 2 && words[0] == "go" && words[1] == "test" {
		if parseGoTest(res, out, root) {
			return
		}
	}
	parseGeneric(res, out, root)
}

type testKey struct{ pkg, test string }

type goEvent struct {
	ImportPath string
	Action     string
	Package    string
	Test       string
	Output     string
	Elapsed    float64
}

// parseGoTest reads `go test -json`. It returns false when the output is not
// that stream (a build that failed before any test started prints plain text).
func parseGoTest(res *Result, out []byte, root string) bool {
	mod := modulePath(root)
	output := map[testKey][]string{}
	var order []testKey
	failed := map[testKey]bool{}
	pkgFailed := map[string]bool{}
	var pkgs, passed, failedN int
	events := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var e goEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Action == "" {
			continue
		}
		events++
		switch e.Action {
		case "build-output": // go 1.24 and later report compile errors apart from tests
			e.Action, e.Package = "output", strings.Fields(e.ImportPath + " ")[0]
		case "build-fail":
			continue
		}
		k := testKey{e.Package, e.Test}
		switch e.Action {
		case "output":
			if _, seen := output[k]; !seen {
				order = append(order, k)
			}
			output[k] = append(output[k], strings.TrimRight(e.Output, "\r\n"))
		case "fail":
			if e.Test != "" {
				failed[k] = true
				failedN++
			} else {
				pkgFailed[e.Package] = true
				pkgs++
			}
		case "pass":
			if e.Test != "" {
				passed++
			} else {
				pkgs++
			}
		}
	}
	if events == 0 {
		return false
	}
	res.Summary = fmt.Sprintf("%d failed, %d passed in %d packages", failedN, passed, pkgs)
	if res.Exit == 0 && failedN == 0 {
		res.Summary = fmt.Sprintf("ok: %d passed in %d packages", passed, pkgs)
	}
	seen := map[testKey]bool{}
	for _, k := range order {
		if len(res.Failures) == maxFailures {
			break
		}
		if k.test == "" || !failed[k] || seen[k] {
			continue
		}
		seen[k] = true
		// A parent test fails when a subtest does; its own output is the subtests'.
		if hasFailedSubtest(k.test, k.pkg, failed) {
			continue
		}
		f := Failure{Name: k.test, Package: relPackage(mod, k.pkg)}
		f.Message, f.Frames = digest(output[k], filepath.Join(root, f.Package), root, true)
		res.Failures = append(res.Failures, f)
	}
	for _, k := range order {
		// A package that failed with no failed test: the build did.
		if k.test == "" && pkgFailed[k.pkg] && !anyFailed(failed, k.pkg) && len(res.Failures) < maxFailures {
			f := Failure{Name: "build", Package: relPackage(mod, k.pkg)}
			f.Message, f.Frames = digest(output[k], filepath.Join(root, f.Package), root, true)
			res.Failures = append(res.Failures, f)
			pkgFailed[k.pkg] = false
		}
	}
	return true
}

func hasFailedSubtest(test, pkg string, failed map[testKey]bool) bool {
	for k := range failed {
		if k.pkg == pkg && strings.HasPrefix(k.test, test+"/") {
			return true
		}
	}
	return false
}

func anyFailed(failed map[testKey]bool, pkg string) bool {
	for k := range failed {
		if k.pkg == pkg {
			return true
		}
	}
	return false
}

var modRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

func modulePath(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	if m := modRe.FindSubmatch(raw); m != nil {
		return string(m[1])
	}
	return ""
}

func relPackage(mod, pkg string) string {
	if mod != "" && pkg == mod {
		return "."
	}
	if mod != "" && strings.HasPrefix(pkg, mod+"/") {
		return strings.TrimPrefix(pkg, mod+"/")
	}
	return pkg
}

// digest drops the noise from the lines of one failure and returns the message
// that remains with the project frames it mentions. dir is where a bare file name
// such as x_test.go is meant to be found.
func digest(lines []string, dir, root string, bareNames bool) (string, []Frame) {
	var keep []string
	var frames []Frame
	seen := map[Frame]bool{}
	for _, raw := range lines {
		l := ansiRe.ReplaceAllString(raw, "")
		t := strings.TrimSpace(l)
		switch {
		case t == "", strings.HasPrefix(t, "=== "), strings.HasPrefix(t, "--- PASS"), t == "FAIL", strings.HasPrefix(t, "FAIL\t"), strings.HasPrefix(t, "exit status"):
			continue
		case vendorRe.MatchString(l):
			continue
		}
		for _, f := range framesIn(l, dir, root, bareNames) {
			if !seen[f] {
				seen[f] = true
				frames = append(frames, f)
			}
		}
		keep = append(keep, strings.TrimRight(l, " \t"))
	}
	if len(keep) > maxMessageLines {
		keep = append(keep[:maxMessageLines], fmt.Sprintf("... [%d more lines]", len(keep)-maxMessageLines))
	}
	return strings.Join(keep, "\n"), frames
}

func framesIn(line, dir, root string, bareNames bool) []Frame {
	var out []Frame
	add := func(file string, n string) {
		var ln int
		fmt.Sscan(n, &ln)
		if rel, ok := projectPath(file, dir, root, bareNames); ok && ln > 0 {
			out = append(out, Frame{rel, ln})
		}
	}
	for _, m := range frameRe.FindAllStringSubmatch(line, -1) {
		add(m[1], m[2])
	}
	for _, m := range pyFrame.FindAllStringSubmatch(line, -1) {
		add(m[1], m[2])
	}
	return out
}

// projectPath maps a path from a log to a file under root, or reports that it
// is not one of the project's.
func projectPath(file, dir, root string, bareNames bool) (string, bool) {
	file = filepath.ToSlash(file)
	if filepath.IsAbs(file) {
		rel, err := filepath.Rel(root, filepath.FromSlash(file))
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}
	candidates := []string{file}
	if bareNames && !strings.Contains(file, "/") {
		if rel, err := filepath.Rel(root, filepath.Join(dir, file)); err == nil {
			candidates = []string{filepath.ToSlash(rel)}
		}
	}
	for _, c := range candidates {
		c = strings.TrimPrefix(c, "./")
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(c))); err == nil {
			return c, true
		}
	}
	return "", false
}

var summaryRe = regexp.MustCompile(`(?i)(\d+ (?:passed|failed|errors?|skipped)|tests?:\s*\d+|failures?:\s*\d+|OK \(\d+|ran \d+ tests?|\d+ examples?, \d+ failures?|test result:)`)

// parseGeneric handles the output of runners with no structured form: it drops
// control codes, progress dots and vendor frames, keeps the first failure
// blocks, and picks out the summary line.
func parseGeneric(res *Result, out []byte, root string) {
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		l = ansiRe.ReplaceAllString(strings.TrimRight(l, "\r"), "")
		if i := strings.LastIndex(l, "\r"); i >= 0 {
			l = l[i+1:]
		}
		lines = append(lines, l)
	}
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-60; i-- {
		if summaryRe.MatchString(lines[i]) {
			res.Summary = strings.TrimSpace(lines[i])
			break
		}
	}
	if res.Summary == "" {
		res.Summary = fmt.Sprintf("exit %d", res.Exit)
	}
	if res.Exit == 0 {
		return
	}
	start := -1
	for i, l := range lines {
		t := strings.ToLower(l)
		if strings.Contains(t, "failure") || strings.Contains(t, "error") || strings.Contains(t, "failed") || strings.Contains(t, "exception") || strings.HasPrefix(strings.TrimSpace(l), "FAIL") {
			if !strings.HasPrefix(strings.TrimSpace(l), ".") {
				start = i
				break
			}
		}
	}
	if start < 0 {
		start = max(0, len(lines)-maxMessageLines)
	}
	msg, frames := digest(lines[start:], root, root, false)
	res.Failures = []Failure{{Name: "failure", Message: msg, Frames: frames}}
}
