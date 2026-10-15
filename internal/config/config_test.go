package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".onfly"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".onfly", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func validYAML() string {
	return `
project: demo
database:
  driver: sqlite
  dsn: .onfly/vector.db
embedder:
  provider: ollama
  endpoint: http://localhost:11434
  model: nomic-embed-text
  dimensions: 768
rules: []
indexer:
  include: ["src/**/*"]
  exclude: ["vendor"]
server:
  transport: stdio
  log_level: info
  log_file: .onfly/server.log
`
}

func TestNoGitKeepsNearestConfig(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "project: root\nworkspace: rootspace\n"+validBody())
	sub := filepath.Join(root, "services", "api")
	writeConfig(t, sub, "project: api\nworkspace: backend\n"+validBody())
	cfg, err := Load(sub, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Project != "api" || cfg.Workspace != "backend" {
		t.Fatalf("%+v", cfg)
	}
}

func TestGitRootMergesAndPrefixesInclude(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, `project: mono
database:
  driver: sqlite
  dsn: .onfly/vector.db
embedder:
  provider: ollama
  endpoint: http://localhost:11434
  model: nomic-embed-text
  dimensions: 4
server:
  transport: stdio
  log_level: info
  log_file: .onfly/server.log
indexer:
  include: ["docs/**/*.md"]
  exclude: ["var"]
`)
	sub := filepath.Join(root, "services", "api")
	writeConfig(t, sub, `workspace: backend
indexer:
  include: ["**/*.go"]
  exclude: ["tmp"]
`)
	cfg, err := Load(sub, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace != "backend" || cfg.Project != "mono" {
		t.Fatalf("identity %+v", cfg)
	}
	if cfg.ProjectRoot != root {
		t.Fatalf("root %s", cfg.ProjectRoot)
	}
	if cfg.Database.DSN != filepath.Join(root, ".onfly", "vector.db") {
		t.Fatalf("dsn %s", cfg.Database.DSN)
	}
	if len(cfg.Indexer.Include) != 1 || cfg.Indexer.Include[0] != "services/api/**/*.go" {
		t.Fatalf("include %v", cfg.Indexer.Include)
	}
	if !hasString(cfg.Indexer.Exclude, "var") || !hasString(cfg.Indexer.Exclude, "tmp") {
		t.Fatalf("exclude %v", cfg.Indexer.Exclude)
	}
	if cfg.Embedder.MaxInputTokens != 6000 || cfg.Indexer.DebounceMS != 2000 || cfg.Server.MaxTokens != 8192 {
		t.Fatalf("defaults %+v %+v", cfg.Embedder, cfg.Server)
	}
	only, err := Load(sub, filepath.Join(root, ".onfly", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if only.Project != "mono" || only.Workspace != "" {
		t.Fatalf("explicit %+v", only)
	}
}

func validBody() string {
	return `
database:
  driver: sqlite
  dsn: .onfly/vector.db
embedder:
  provider: ollama
  endpoint: http://localhost:11434
  model: nomic-embed-text
  dimensions: 4
indexer:
  include: ["src/**/*"]
server:
  transport: stdio
  log_level: info
  log_file: .onfly/server.log
`
}

func hasString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestLoadWalksParentsAndResolvesPaths(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, validYAML())
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(nested, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectRoot != root {
		t.Fatalf("root = %s", cfg.ProjectRoot)
	}
	if cfg.Database.DSN != filepath.Join(root, ".onfly", "vector.db") {
		t.Fatalf("dsn = %s", cfg.Database.DSN)
	}
	if cfg.Server.LogFile != filepath.Join(root, ".onfly", "server.log") {
		t.Fatalf("log = %s", cfg.Server.LogFile)
	}
}

func TestExplicitRelativeConfigResolvesOnce(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "svc")
	writeConfig(t, sub, validYAML())
	t.Chdir(root)
	cfg, err := Load(root, filepath.Join("svc", ".onfly", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectRoot != sub {
		t.Fatalf("root = %s", cfg.ProjectRoot)
	}
	want := filepath.Join(sub, ".onfly", "vector.db")
	if cfg.Database.DSN != want {
		t.Fatalf("dsn = %s", cfg.Database.DSN)
	}
	if cfg.Server.LogFile != filepath.Join(sub, ".onfly", "server.log") {
		t.Fatalf("log = %s", cfg.Server.LogFile)
	}
}

func TestMergeFoldsParserExtensionCase(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, `
project: mono
database: {driver: sqlite, dsn: .onfly/vector.db}
embedder: {provider: ollama, model: m, dimensions: 4}
server: {transport: stdio, log_file: .onfly/server.log}
indexer:
  include: ["**/*"]
  parsers:
    - extensions: [".PHP"]
      type: stdio
      command: php parent.php
`)
	sub := filepath.Join(root, "svc")
	writeConfig(t, sub, `
indexer:
  include: ["**/*"]
  parsers:
    - extensions: [".php"]
      type: stdio
      command: php child.php
`)
	cfg, err := Load(sub, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Indexer.Parsers) != 1 || cfg.Indexer.Parsers[0].Command != "php child.php" {
		t.Fatalf("%+v", cfg.Indexer.Parsers)
	}
	if len(cfg.Indexer.Parsers[0].Extensions) != 1 || cfg.Indexer.Parsers[0].Extensions[0] != ".php" {
		t.Fatalf("%+v", cfg.Indexer.Parsers[0].Extensions)
	}
}

func TestLoadExpandsEnv(t *testing.T) {
	t.Setenv("ONFLY_DB_PASSWORD", "s3cret")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	root := t.TempDir()
	writeConfig(t, root, `
project: demo
database:
  driver: postgres
  dsn: postgres://user:${ONFLY_DB_PASSWORD}@localhost:5432/onfly
embedder:
  provider: openai
  model: text-embedding-3-small
  dimensions: 768
  api_key: ${OPENAI_API_KEY}
indexer:
  include: ["src/**/*"]
server:
  transport: stdio
  log_file: .onfly/server.log
`)
	cfg, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg.Database.DSN, "${") || !strings.Contains(cfg.Database.DSN, "s3cret") {
		t.Fatalf("dsn = %s", cfg.Database.DSN)
	}
	if cfg.Embedder.APIKey != "sk-test" {
		t.Fatalf("key = %s", cfg.Embedder.APIKey)
	}
}

func TestLoadVoyage(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "vk")
	root := t.TempDir()
	writeConfig(t, root, `
project: demo
database:
  driver: sqlite
  dsn: .onfly/vector.db
embedder:
  provider: voyage
  model: voyage-code-3
  dimensions: 1024
  api_key: ${VOYAGE_API_KEY}
indexer:
  include: ["src/**/*"]
server:
  transport: stdio
  log_file: .onfly/server.log
`)
	cfg, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedder.Provider != "voyage" || cfg.Embedder.APIKey != "vk" || cfg.Embedder.Model != "voyage-code-3" {
		t.Fatalf("%+v", cfg.Embedder)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"dimensions": `
project: demo
database: {driver: sqlite, dsn: .onfly/vector.db}
embedder: {provider: ollama, model: m}
indexer: {include: ["src/**/*"]}
server: {transport: stdio, log_file: .onfly/server.log}
`,
		"include": `
project: demo
database: {driver: sqlite, dsn: .onfly/vector.db}
embedder: {provider: ollama, model: m, dimensions: 3}
indexer: {include: []}
server: {transport: stdio, log_file: .onfly/server.log}
`,
		"mysql": `
project: demo
database: {driver: mysql, dsn: x}
embedder: {provider: ollama, model: m, dimensions: 3}
indexer: {include: ["src/**/*"]}
server: {transport: stdio, log_file: .onfly/server.log}
`,
		"http": `
project: demo
database: {driver: sqlite, dsn: .onfly/vector.db}
embedder: {provider: ollama, model: m, dimensions: 3}
indexer: {include: ["src/**/*"]}
server: {transport: http, log_file: .onfly/server.log}
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, body)
			_, err := Load(root, "")
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRulesEngineDefaultsAndMerge(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, validYAML())
	cfg, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RulesEngine.MaxTokenBudgetPercent != 15 || cfg.RulesEngine.AutoDiscovery.Enabled != nil {
		t.Fatalf("defaults %+v", cfg.RulesEngine)
	}
	order := cfg.RulesEngine.AutoDiscovery.FallbackOrder
	if len(order) != 6 || order[0] != ".cursorrules" || order[1] != "AGENTS.md" || order[2] != "CLAUDE.md" || order[3] != "AGENT.md" {
		t.Fatalf("fallback %v", order)
	}

	sub := filepath.Join(root, "svc")
	writeConfig(t, sub, `
rules_engine:
  max_token_budget_percent: 30
  auto_discovery:
    enabled: false
    fallback_order: ["AGENTS.md"]
indexer:
  include: ["**/*"]
`)
	cfg, err = Load(sub, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RulesEngine.MaxTokenBudgetPercent != 30 {
		t.Fatalf("percent %d", cfg.RulesEngine.MaxTokenBudgetPercent)
	}
	if cfg.RulesEngine.AutoDiscovery.Enabled == nil || *cfg.RulesEngine.AutoDiscovery.Enabled {
		t.Fatalf("enabled %+v", cfg.RulesEngine.AutoDiscovery.Enabled)
	}
	if len(cfg.RulesEngine.AutoDiscovery.FallbackOrder) != 1 || cfg.RulesEngine.AutoDiscovery.FallbackOrder[0] != "AGENTS.md" {
		t.Fatalf("order %v", cfg.RulesEngine.AutoDiscovery.FallbackOrder)
	}

	bad := t.TempDir()
	writeConfig(t, bad, `
database: {driver: sqlite, dsn: .onfly/vector.db}
embedder: {provider: ollama, model: m, dimensions: 4}
indexer: {include: ["src/**/*"]}
server: {transport: stdio, log_file: .onfly/server.log}
rules_engine:
  max_token_budget_percent: 150
`)
	if _, err := Load(bad, ""); err == nil {
		t.Fatal("expected percent error")
	}
}

func TestExpandOneEscapesDollar(t *testing.T) {
	t.Setenv("ONFLY_T_PW", "x")
	got, err := expandOne("pg://u:pa$$word@h/${ONFLY_T_PW}")
	if err != nil || got != "pg://u:pa$word@h/x" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestMergeParsersKeepsScopesApart(t *testing.T) {
	parent := []ParserConfig{
		{Extensions: []string{".php"}, Type: "wasm", Path: "modern.wasm", Include: []string{"app/**"}},
		{Extensions: []string{".php"}, Type: "wasm", Path: "legacy.wasm", Include: []string{"legacy/**"}},
	}
	child := []ParserConfig{{Extensions: []string{".php"}, Type: "wasm", Path: "legacy2.wasm", Include: []string{"legacy/**"}}}
	got := mergeParsers(parent, child)
	if len(got) != 2 || got[0].Path != "modern.wasm" || got[1].Path != "legacy2.wasm" {
		t.Fatalf("%+v", got)
	}
	if got[0].Extensions[0] != ".php" || got[0].Include[0] != "app/**" {
		t.Fatalf("scope or extension lost: %+v", got[0])
	}
}

func TestSearchRankingKnobsLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, strings.Replace(validYAML(), "indexer:\n", "indexer:\n  summaries: {provider: ollama, model: m}\n", 1)+`
search:
  word_weight: 0
  field_weight: 2
  rrf_k: 20
  group_by: file
  dedupe: true
  expand: {provider: ollama, model: m, timeout_ms: 2000}
`)
	cfg, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Search
	if s.WordWeight == nil || *s.WordWeight != 0 || s.FieldWeight == nil || *s.FieldWeight != 2 ||
		s.RRFK != 20 || s.GroupBy != "file" || !s.Dedupe || s.Expand.Model != "m" || s.Expand.Timeout().Milliseconds() != 2000 ||
		cfg.Indexer.Summaries.Provider != "ollama" {
		t.Fatalf("%+v %+v", s, cfg.Indexer.Summaries)
	}
	for name, extra := range map[string]string{
		"group_by":       "search:\n  group_by: bogus\n",
		"negative field": "search:\n  field_weight: -1\n",
		"expand model":   "search:\n  expand: {provider: ollama}\n",
		"expand vendor":  "search:\n  expand: {provider: bogus, model: m}\n",
	} {
		d := t.TempDir()
		writeConfig(t, d, validYAML()+extra)
		if _, err := Load(d, ""); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
}
