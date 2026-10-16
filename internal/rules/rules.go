package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/template"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/tokens"
	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v3"
)

const defaultPercent = 15

var defaultFallback = []string{
	".cursorrules",
	"AGENTS.md",
	"CLAUDE.md",
	"AGENT.md",
	".clinerules",
	".github/copilot-instructions.md",
}

var langExt = map[string][]string{
	"go":         {".go"},
	"php":        {".php", ".inc"},
	"rust":       {".rs"},
	"typescript": {".ts", ".tsx"},
	"javascript": {".js", ".jsx"},
	"python":     {".py"},
	"ruby":       {".rb"},
	"java":       {".java"},
}

type Query struct {
	Targets []Target
}

type Target struct {
	Path string
	Type string
}

type Engine struct {
	rules    []Rule
	readErr  error
	data     templateData
	percent  int
	fallback []string
	root     string
}

type Rule struct {
	Name     string
	Scope    []string
	Ignore   []string
	Priority int
	Body     string
	Path     string
	Err      error
}

type templateData struct {
	Project       projectData
	Env           envData
	ActiveParsers parserNames
}

type projectData struct {
	Name string
}

type envData struct {
	OS   string
	Arch string
}

type parserNames []string

func (p parserNames) String() string {
	names := []string(p)
	if names == nil {
		names = []string{}
	}
	b, err := json.Marshal(names)
	if err != nil {
		return "[]"
	}
	return string(b)
}

type frontmatter struct {
	Name     string   `yaml:"name"`
	Scope    []string `yaml:"scope"`
	Ignore   []string `yaml:"ignore"`
	Priority int      `yaml:"priority"`
}

func Load(cfg config.Config) *Engine {
	percent := cfg.RulesEngine.MaxTokenBudgetPercent
	if percent <= 0 {
		percent = defaultPercent
	}
	fallback := cfg.RulesEngine.AutoDiscovery.FallbackOrder
	if len(fallback) == 0 {
		fallback = append([]string{}, defaultFallback...)
	}
	e := &Engine{
		data:     templateDataFor(cfg),
		percent:  percent,
		fallback: fallback,
		root:     cfg.ProjectRoot,
	}
	seen := map[string]bool{}
	var readErrs []string
	add := func(path string, explicit bool) {
		key := absKey(path)
		if seen[key] {
			return
		}
		seen[key] = true
		body, err := os.ReadFile(path)
		if err != nil {
			if explicit {
				readErrs = append(readErrs, fmt.Sprintf("rule %s: %s", path, err.Error()))
			}
			return
		}
		e.rules = append(e.rules, parseRule(path, string(body), e.data))
	}
	if cfg.ProjectRoot != "" {
		dir := filepath.Join(cfg.ProjectRoot, ".onfly", "rules")
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			readErrs = append(readErrs, err.Error())
		}
		var names []string
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
				continue
			}
			names = append(names, entry.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			add(filepath.Join(dir, name), true)
		}
	}
	for _, path := range cfg.Rules {
		add(path, true)
	}
	if cfg.ProjectRoot != "" && len(seen) == 0 && discoveryOn(cfg.RulesEngine.AutoDiscovery) {
		for _, rel := range fallback {
			full, ok := fallbackPath(cfg.ProjectRoot, rel)
			if !ok {
				continue
			}
			if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
				continue
			}
			add(full, false)
			break
		}
	}
	if len(readErrs) > 0 {
		e.readErr = fmt.Errorf("%s", strings.Join(readErrs, "; "))
	}
	sort.SliceStable(e.rules, func(i, j int) bool {
		if e.rules[i].Priority != e.rules[j].Priority {
			return e.rules[i].Priority > e.rules[j].Priority
		}
		if e.rules[i].Name != e.rules[j].Name {
			return e.rules[i].Name < e.rules[j].Name
		}
		return e.rules[i].Path < e.rules[j].Path
	})
	return e
}

func (e *Engine) ReadError() error {
	if e == nil {
		return nil
	}
	return e.readErr
}

func (e *Engine) Inject(q Query, maxTokens int) string {
	if e == nil {
		return ""
	}
	var parts []string
	for _, rule := range e.rules {
		if rule.Err != nil || !rule.matches(q) {
			continue
		}
		parts = append(parts, rule.text())
	}
	return fitText(parts, e.budget(maxTokens))
}

func (e *Engine) InjectNew(q Query, maxTokens int, seen map[string]bool) (string, int) {
	if e == nil {
		return "", 0
	}
	var parts []string
	var keys []string
	for _, rule := range e.rules {
		if rule.Err != nil || seen[rule.Path] || !rule.matches(q) {
			continue
		}
		parts = append(parts, rule.text())
		keys = append(keys, rule.Path)
	}
	text := fitText(parts, e.budget(maxTokens))
	if text == "" {
		return "", 0
	}
	for _, k := range keys {
		seen[k] = true
	}
	return text, len(keys)
}

type Report struct {
	Tokens  int
	Budget  int
	Syntax  []string
	Warning string
}

func (e *Engine) Report(maxTokens int) Report {
	if e == nil {
		return Report{}
	}
	rep := Report{Budget: e.budget(maxTokens)}
	var parts []string
	for _, rule := range e.rules {
		if rule.Err != nil {
			rep.Syntax = append(rep.Syntax, fmt.Sprintf("%s: %s", displayPath(e.root, rule.Path), rule.Err.Error()))
			continue
		}
		parts = append(parts, rule.text())
	}
	rep.Tokens = count(joinParts(parts))
	if len(rep.Syntax) > 0 || rep.Tokens <= rep.Budget {
		return rep
	}
	msg := fmt.Sprintf("%d tokens exceed the budget of %d; lower-priority rules are truncated", rep.Tokens, rep.Budget)
	if names := e.overlapNames(); len(names) > 0 {
		msg += "; overlapping scopes: " + strings.Join(names, ", ")
	}
	rep.Warning = msg
	return rep
}

func (e *Engine) budget(maxTokens int) int {
	percent := e.percent
	if percent <= 0 {
		percent = defaultPercent
	}
	if percent > 100 {
		percent = 100
	}
	if maxTokens < 1 {
		return 0
	}
	return maxTokens * percent / 100
}

func (e *Engine) overlapNames() []string {
	seen := map[string]bool{}
	var names []string
	add := func(rule Rule) {
		n := filepath.Base(rule.Path)
		if seen[n] {
			return
		}
		seen[n] = true
		names = append(names, n)
	}
	for i := range e.rules {
		if e.rules[i].Err != nil {
			continue
		}
		for j := i + 1; j < len(e.rules); j++ {
			if e.rules[j].Err != nil || !overlaps(e.rules[i], e.rules[j]) {
				continue
			}
			add(e.rules[i])
			add(e.rules[j])
		}
	}
	sort.Strings(names)
	return names
}

func overlaps(a, b Rule) bool {
	if a.isGlobal() || b.isGlobal() {
		return true
	}
	for _, sa := range a.Scope {
		for _, sb := range b.Scope {
			if sa == sb {
				return true
			}
		}
	}
	return false
}

func (r Rule) text() string {
	name := r.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(r.Path), filepath.Ext(r.Path))
	}
	return "# " + name + "\n\n" + strings.TrimSpace(r.Body) + "\n"
}

func (r Rule) matches(q Query) bool {
	if len(q.Targets) == 0 {
		return r.isGlobal()
	}
	for _, t := range q.Targets {
		path := filepath.ToSlash(t.Path)
		if path != "" && r.ignored(path) {
			continue
		}
		if r.isGlobal() || (path != "" && r.pathScoped(path)) || (t.Type != "" && r.typeScoped(t.Type)) {
			return true
		}
	}
	return false
}

func (r Rule) isGlobal() bool {
	if len(r.Scope) == 0 {
		return true
	}
	for _, entry := range r.Scope {
		if strings.TrimSpace(entry) == "*" {
			return true
		}
	}
	return false
}

func (r Rule) ignored(path string) bool {
	for _, pattern := range r.Ignore {
		if globHit(pattern, path) {
			return true
		}
	}
	return false
}

func (r Rule) pathScoped(path string) bool {
	for _, entry := range r.Scope {
		entry = strings.TrimSpace(entry)
		if entry == "" || entry == "*" {
			continue
		}
		if exts, ok := langExt[strings.ToLower(entry)]; ok {
			ext := strings.ToLower(filepath.Ext(path))
			for _, want := range exts {
				if ext == want {
					return true
				}
			}
			continue
		}
		if strings.HasPrefix(entry, ".") && !strings.ContainsAny(entry, "*?[") {
			if strings.EqualFold(filepath.Ext(path), entry) {
				return true
			}
			continue
		}
		if isGlob(entry) && globHit(entry, path) {
			return true
		}
	}
	return false
}

func (r Rule) typeScoped(typ string) bool {
	for _, entry := range r.Scope {
		entry = strings.TrimSpace(entry)
		if entry == "" || entry == "*" || isGlob(entry) {
			continue
		}
		if _, ok := langExt[strings.ToLower(entry)]; ok {
			continue
		}
		if strings.HasPrefix(entry, ".") {
			continue
		}
		if strings.EqualFold(entry, typ) {
			return true
		}
	}
	return false
}

func isGlob(entry string) bool {
	return strings.ContainsAny(entry, "*?[") || strings.Contains(entry, "/")
}

func globHit(pattern, path string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	path = filepath.ToSlash(path)
	if pattern == "" || path == "" {
		return false
	}
	if doublestar.MatchUnvalidated(pattern, path) {
		return true
	}
	prefix := pattern
	if i := strings.IndexAny(prefix, "*?["); i >= 0 {
		prefix = prefix[:i]
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return false
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func parseRule(path, raw string, data templateData) Rule {
	rule := Rule{Path: path}
	meta, body, has, err := splitFrontmatter(raw)
	if err != nil {
		rule.Err = err
		return rule
	}
	if has {
		var fm frontmatter
		if err := yaml.Unmarshal(meta, &fm); err != nil {
			rule.Err = err
			return rule
		}
		rule.Name = fm.Name
		rule.Scope = fm.Scope
		rule.Ignore = fm.Ignore
		rule.Priority = fm.Priority
	}
	rendered, err := render(body, data)
	if err != nil {
		rule.Err = err
		return rule
	}
	rule.Body = rendered
	return rule
}

func splitFrontmatter(raw string) ([]byte, string, bool, error) {
	s := strings.TrimPrefix(raw, "\uFEFF")
	if !strings.HasPrefix(s, "---") {
		return nil, raw, false, nil
	}
	rest := s[3:]
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	} else {
		return nil, raw, false, nil
	}
	lines := strings.Split(rest, "\n")
	for i, line := range lines {
		if strings.TrimRight(line, "\r") == "---" {
			return []byte(strings.Join(lines[:i], "\n")), strings.Join(lines[i+1:], "\n"), true, nil
		}
	}
	return nil, "", true, fmt.Errorf("frontmatter is not closed")
}

func render(body string, data templateData) (string, error) {
	t, err := template.New("rule").Option("missingkey=error").Parse(body)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func templateDataFor(cfg config.Config) templateData {
	name := ""
	if cfg.ProjectRoot != "" {
		name = moduleName(cfg.ProjectRoot)
	}
	if name == "" && cfg.ProjectRoot != "" {
		name = jsonName(filepath.Join(cfg.ProjectRoot, "composer.json"))
	}
	if name == "" && cfg.ProjectRoot != "" {
		name = jsonName(filepath.Join(cfg.ProjectRoot, "package.json"))
	}
	if name == "" {
		name = cfg.Project
	}
	return templateData{
		Project:       projectData{Name: name},
		Env:           envData{OS: runtime.GOOS, Arch: runtime.GOARCH},
		ActiveParsers: parserLanguages(cfg.Indexer.Parsers),
	}
}

func moduleName(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return ""
}

func jsonName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return ""
	}
	name, _ := doc["name"].(string)
	return name
}

func parserLanguages(parsers []config.ParserConfig) parserNames {
	extLang := map[string]string{}
	for lang, exts := range langExt {
		for _, ext := range exts {
			extLang[ext] = lang
		}
	}
	seen := map[string]bool{}
	var names []string
	for _, p := range parsers {
		for _, ext := range p.Extensions {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext != "" && !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			name := extLang[ext]
			if name == "" {
				name = strings.TrimPrefix(ext, ".")
			}
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func discoveryOn(a config.AutoDiscovery) bool {
	return a.Enabled == nil || *a.Enabled
}

func fallbackPath(root, rel string) (string, bool) {
	rel = filepath.Clean(rel)
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(root, rel), true
}

func absKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func displayPath(root, path string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return path
}

func fitText(parts []string, budget int) string {
	if budget <= 0 || len(parts) == 0 {
		return ""
	}
	var kept []string
	for _, part := range parts {
		trial := append(append([]string{}, kept...), part)
		if count(joinParts(trial)) <= budget {
			kept = trial
			continue
		}
		break
	}
	if len(kept) == 0 {
		cut := truncateToTokens(parts[0], budget)
		if strings.TrimSpace(cut) == "" {
			return ""
		}
		kept = []string{cut}
	}
	text := joinParts(kept)
	omitted := len(parts) - len(kept)
	if omitted > 0 {
		note := fmt.Sprintf("\n\n// [Truncated: %d rules omitted due to context limits.]", omitted)
		if count(text+note) <= budget {
			text += note
		}
	}
	return text
}

func joinParts(parts []string) string {
	return strings.Join(parts, "\n\n")
}

func count(s string) int {
	n, err := tokens.Count(s)
	if err != nil {
		return 0
	}
	return n
}

func truncateToTokens(s string, budget int) string {
	rs := []rune(s)
	lo, hi := 0, len(rs)
	best := ""
	for lo <= hi {
		mid := (lo + hi) / 2
		cand := string(rs[:mid])
		if count(cand) <= budget {
			best = cand
			lo = mid + 1
			continue
		}
		hi = mid - 1
	}
	return best
}
