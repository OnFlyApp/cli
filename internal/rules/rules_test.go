package rules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/tokens"
)

func TestFrontmatterOrderAndTemplate(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "low.md", "---\nname: Low\nscope: [\"*\"]\npriority: 1\n---\nSECOND\n")
	writeRule(t, dir, "high.md", "---\nname: High\nscope: [\"*\"]\npriority: 50\n---\nFIRST {{ .Project.Name }} {{ .Env.OS }} {{ .ActiveParsers }}\n")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/acme/demo\n\ngo 1.27.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ProjectRoot: dir,
		Project:     "ignored",
		Indexer: config.Indexer{Parsers: []config.ParserConfig{
			{Extensions: []string{".php"}},
			{Extensions: []string{".go"}},
		}},
		RulesEngine: config.RulesEngine{MaxTokenBudgetPercent: 100},
	}
	eng := Load(cfg)
	got := eng.Inject(Query{}, 10000)
	if strings.Index(got, "FIRST") > strings.Index(got, "SECOND") || !strings.Contains(got, "FIRST") {
		t.Fatalf("order:\n%s", got)
	}
	for _, needle := range []string{"github.com/acme/demo", runtime.GOOS, `["go","php"]`} {
		if !strings.Contains(got, needle) {
			t.Fatalf("missing %s\n%s", needle, got)
		}
	}
	if strings.Contains(got, "ignored") {
		t.Fatalf("config project used\n%s", got)
	}
}

func TestScopeIgnoreAndSyntax(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "db.md", "---\nname: Database\nscope: [\"**/*.go\", \"go\"]\nignore: [\"**/*_test.go\"]\npriority: 100\n---\nNO RAW SQL\n")
	writeRule(t, dir, "iface.md", "---\nname: Interfaces\nscope: [\"interface\", \"trait\"]\npriority: 80\n---\nDOCUMENT THE INTERFACE\n")
	writeRule(t, dir, "react.md", "---\nname: React\nscope: [\"frontend/src/**\"]\npriority: 40\n---\nREACT RULES\n")
	writeRule(t, dir, "broken.md", "---\nscope: [\n")
	eng := Load(config.Config{ProjectRoot: dir, RulesEngine: config.RulesEngine{MaxTokenBudgetPercent: 100}})
	if !strings.Contains(eng.Inject(Query{Targets: []Target{{Path: "src/a.go"}}}, 10000), "NO RAW SQL") {
		t.Fatal("go file")
	}
	if strings.Contains(eng.Inject(Query{Targets: []Target{{Path: "src/a_test.go"}}}, 10000), "NO RAW SQL") {
		t.Fatal("test file should be ignored")
	}
	if strings.Contains(eng.Inject(Query{Targets: []Target{{Path: "src/a.go", Type: "function"}}}, 10000), "DOCUMENT THE INTERFACE") {
		t.Fatal("function is not an interface")
	}
	got := eng.Inject(Query{Targets: []Target{{Path: "src/a.go", Type: "interface"}}}, 10000)
	if !strings.Contains(got, "DOCUMENT THE INTERFACE") || !strings.Contains(got, "NO RAW SQL") {
		t.Fatalf("interface query:\n%s", got)
	}
	if !strings.Contains(eng.Inject(Query{Targets: []Target{{Path: "frontend/src"}}}, 10000), "REACT RULES") {
		t.Fatal("directory prefix")
	}
	if strings.Contains(eng.Inject(Query{Targets: []Target{{Path: "backend/main.go", Type: "function"}}}, 10000), "REACT RULES") {
		t.Fatal("other directory")
	}
	rep := eng.Report(8192)
	if len(rep.Syntax) != 1 || !strings.Contains(rep.Syntax[0], "broken.md") {
		t.Fatalf("syntax %#v", rep)
	}
	if strings.Contains(eng.Inject(Query{}, 10000), "NO RAW SQL") {
		t.Fatal("path rule is not global")
	}
}

func TestFallbackStopsAtFirstMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".cursorrules"), []byte("cursor-rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("agent-rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := Load(config.Config{ProjectRoot: dir})
	got := eng.Inject(Query{}, 8192)
	if !strings.Contains(got, "cursor-rule") || strings.Contains(got, "agent-rule") {
		t.Fatalf("fallback:\n%s", got)
	}

	off := false
	disabled := Load(config.Config{ProjectRoot: dir, RulesEngine: config.RulesEngine{AutoDiscovery: config.AutoDiscovery{Enabled: &off}}})
	if disabled.Inject(Query{}, 8192) != "" {
		t.Fatal("discovery off should ignore fallback files")
	}

	writeRule(t, dir, "local.md", "explicit-rule\n")
	explicit := Load(config.Config{ProjectRoot: dir})
	got = explicit.Inject(Query{}, 8192)
	if !strings.Contains(got, "explicit-rule") || strings.Contains(got, "cursor-rule") {
		t.Fatalf("explicit wins:\n%s", got)
	}
}

func TestFallbackPrefersAgentsAndClaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents-rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("agent-rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude-rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load(config.Config{ProjectRoot: dir}).Inject(Query{}, 8192)
	if !strings.Contains(got, "agents-rule") || strings.Contains(got, "agent-rule") || strings.Contains(got, "claude-rule") {
		t.Fatalf("fallback:\n%s", got)
	}

	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude-rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("agent-rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = Load(config.Config{ProjectRoot: dir}).Inject(Query{}, 8192)
	if !strings.Contains(got, "claude-rule") || strings.Contains(got, "agent-rule") {
		t.Fatalf("claude:\n%s", got)
	}
}

func TestBudgetKeepsHigherPriority(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "high.md", "---\nname: High\nscope: [\"*\"]\npriority: 100\n---\nkeep-the-database-rule\n")
	one := Load(config.Config{ProjectRoot: dir, RulesEngine: config.RulesEngine{MaxTokenBudgetPercent: 100}})
	high := one.Inject(Query{}, 10000)
	n, err := tokens.Count(high)
	if err != nil {
		t.Fatal(err)
	}
	writeRule(t, dir, "low.md", "---\nname: Low\nscope: [\"*\"]\npriority: 1\n---\n"+strings.Repeat("alpha ", 80))
	two := Load(config.Config{ProjectRoot: dir, RulesEngine: config.RulesEngine{MaxTokenBudgetPercent: 100}})
	got := two.Inject(Query{}, n)
	if strings.Contains(got, "alpha") || !strings.Contains(got, "keep-the-database-rule") {
		t.Fatalf("budget:\n%s", got)
	}
	rep := two.Report(n)
	if rep.Warning == "" || !strings.Contains(rep.Warning, "overlapping scopes") {
		t.Fatalf("warning %q tokens %d budget %d", rep.Warning, rep.Tokens, rep.Budget)
	}
	wide := two.Report(100000)
	if wide.Warning != "" {
		t.Fatalf("under budget warned: %s", wide.Warning)
	}
}

func TestProjectNameFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"acme/web"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRule(t, dir, "a.md", "name={{ .Project.Name }}\n")
	got := Load(config.Config{ProjectRoot: dir, Project: "from-config"}).Inject(Query{}, 1000)
	if !strings.Contains(got, "acme/web") {
		t.Fatalf("%s", got)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "package.json"), []byte(`{"name":"web-ui"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRule(t, other, "a.md", "name={{ .Project.Name }}\n")
	got = Load(config.Config{ProjectRoot: other}).Inject(Query{}, 1000)
	if !strings.Contains(got, "web-ui") {
		t.Fatalf("%s", got)
	}

	plain := t.TempDir()
	writeRule(t, plain, "a.md", "name={{ .Project.Name }}\n")
	got = Load(config.Config{ProjectRoot: plain, Project: "from-config"}).Inject(Query{}, 1000)
	if !strings.Contains(got, "from-config") {
		t.Fatalf("%s", got)
	}
}

func TestMissingExplicitRule(t *testing.T) {
	dir := t.TempDir()
	eng := Load(config.Config{ProjectRoot: dir, Rules: []string{filepath.Join(dir, "missing.md")}})
	if eng.ReadError() == nil || !strings.Contains(eng.ReadError().Error(), "missing.md") {
		t.Fatalf("%v", eng.ReadError())
	}
}

func writeRule(t *testing.T, dir, name, body string) {
	t.Helper()
	rules := filepath.Join(dir, ".onfly", "rules")
	if err := os.MkdirAll(rules, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rules, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
