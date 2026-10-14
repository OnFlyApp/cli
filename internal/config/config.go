package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultMaxTokens is the search result budget when server.max_tokens is unset.
const DefaultMaxTokens = 8192

type Config struct {
	Project     string      `yaml:"project"`
	Workspace   string      `yaml:"workspace"`
	Database    Database    `yaml:"database"`
	Embedder    Embedder    `yaml:"embedder"`
	Rules       []string    `yaml:"rules"`
	RulesEngine RulesEngine `yaml:"rules_engine"`
	Indexer     Indexer     `yaml:"indexer"`
	Server      Server      `yaml:"server"`
	ProjectRoot string      `yaml:"-"`
}

type Database struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type Embedder struct {
	Provider       string `yaml:"provider"`
	Endpoint       string `yaml:"endpoint"`
	Model          string `yaml:"model"`
	Dimensions     int    `yaml:"dimensions"`
	APIKey         string `yaml:"api_key"`
	MaxInputTokens int    `yaml:"max_input_tokens"`
}

type Indexer struct {
	Include    []string       `yaml:"include"`
	Exclude    []string       `yaml:"exclude"`
	Parsers    []ParserConfig `yaml:"parsers"`
	DebounceMS int            `yaml:"debounce_ms"`
	// Concurrency is the number of files indexed in parallel (0 or 1: sequential).
	Concurrency int `yaml:"concurrency"`
	// ParseTimeoutMS bounds one parser call; 0 uses the 5 s default.
	ParseTimeoutMS int `yaml:"parse_timeout_ms"`
}

type ParserConfig struct {
	Extensions []string `yaml:"extensions"`
	Type       string   `yaml:"type"`
	Path       string   `yaml:"path"`
	Command    string   `yaml:"command"`
}

type Server struct {
	Transport string `yaml:"transport"`
	LogLevel  string `yaml:"log_level"`
	LogFile   string `yaml:"log_file"`
	MaxTokens int    `yaml:"max_tokens"`
	TraceFile string `yaml:"trace_file"`
}

type RulesEngine struct {
	MaxTokenBudgetPercent int           `yaml:"max_token_budget_percent"`
	AutoDiscovery         AutoDiscovery `yaml:"auto_discovery"`
}

type AutoDiscovery struct {
	Enabled       *bool    `yaml:"enabled"`
	FallbackOrder []string `yaml:"fallback_order"`
}

func Load(startDir, explicitPath string) (Config, error) {
	if explicitPath != "" {
		abs, err := filepath.Abs(explicitPath)
		if err != nil {
			return Config{}, err
		}
		cfg, err := readFile(abs)
		if err != nil {
			return Config{}, err
		}
		return finish(cfg)
	}
	start, err := filepath.Abs(startDir)
	if err != nil {
		return Config{}, err
	}
	gitRoot, hasGit := findGitRoot(start)
	found, err := collectConfigs(start, gitRoot, hasGit)
	if err != nil {
		return Config{}, err
	}
	var merged Config
	for i := len(found) - 1; i >= 0; i-- {
		cfg, err := readFile(found[i])
		if err != nil {
			return Config{}, err
		}
		if i == len(found)-1 {
			merged = cfg
			continue
		}
		merged = mergeConfig(merged, cfg, gitRoot, cfg.ProjectRoot)
	}
	if hasGit {
		merged.ProjectRoot = gitRoot
	}
	return finish(merged)
}

func readFile(path string) (Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: %s: %w", path, err)
	}
	cfg.ProjectRoot = ProjectRoot(path)
	if err := expand(&cfg); err != nil {
		return Config{}, err
	}
	resolve(&cfg)
	return cfg, nil
}

func finish(cfg Config) (Config, error) {
	ApplyDefaults(&cfg)
	resolve(&cfg)
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func ApplyDefaults(cfg *Config) {
	if cfg.Embedder.MaxInputTokens == 0 {
		cfg.Embedder.MaxInputTokens = 6000
	}
	if cfg.Indexer.DebounceMS == 0 {
		cfg.Indexer.DebounceMS = 2000
	}
	if cfg.Server.MaxTokens == 0 {
		cfg.Server.MaxTokens = DefaultMaxTokens
	}
	if cfg.RulesEngine.MaxTokenBudgetPercent == 0 {
		cfg.RulesEngine.MaxTokenBudgetPercent = 15
	}
	if len(cfg.RulesEngine.AutoDiscovery.FallbackOrder) == 0 {
		cfg.RulesEngine.AutoDiscovery.FallbackOrder = []string{
			".cursorrules",
			"AGENTS.md",
			"CLAUDE.md",
			"AGENT.md",
			".clinerules",
			".github/copilot-instructions.md",
		}
	}
}

func findGitRoot(start string) (string, bool) {
	dir := start
	for {
		git := filepath.Join(dir, ".git")
		if st, err := os.Stat(git); err == nil && (st.IsDir() || st.Mode().IsRegular()) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func collectConfigs(start, gitRoot string, hasGit bool) ([]string, error) {
	dir := start
	var found []string
	for {
		candidate := filepath.Join(dir, ".onfly", "config.yaml")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			found = append(found, candidate)
			if !hasGit {
				break
			}
		}
		if hasGit && dir == gitRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("config: .onfly/config.yaml not found from %s", start)
	}
	return found, nil
}

func mergeConfig(parent, child Config, gitRoot, childRoot string) Config {
	out := parent
	if child.Project != "" {
		out.Project = child.Project
	}
	if child.Workspace != "" {
		out.Workspace = child.Workspace
	}
	if child.Database.Driver != "" {
		out.Database.Driver = child.Database.Driver
	}
	if child.Database.DSN != "" {
		out.Database.DSN = child.Database.DSN
	}
	if child.Embedder.Provider != "" {
		out.Embedder.Provider = child.Embedder.Provider
	}
	if child.Embedder.Endpoint != "" {
		out.Embedder.Endpoint = child.Embedder.Endpoint
	}
	if child.Embedder.Model != "" {
		out.Embedder.Model = child.Embedder.Model
	}
	if child.Embedder.Dimensions != 0 {
		out.Embedder.Dimensions = child.Embedder.Dimensions
	}
	if child.Embedder.APIKey != "" {
		out.Embedder.APIKey = child.Embedder.APIKey
	}
	if child.Embedder.MaxInputTokens != 0 {
		out.Embedder.MaxInputTokens = child.Embedder.MaxInputTokens
	}
	if child.Server.Transport != "" {
		out.Server.Transport = child.Server.Transport
	}
	if child.Server.LogLevel != "" {
		out.Server.LogLevel = child.Server.LogLevel
	}
	if child.Server.LogFile != "" {
		out.Server.LogFile = child.Server.LogFile
	}
	if child.Server.MaxTokens != 0 {
		out.Server.MaxTokens = child.Server.MaxTokens
	}
	if child.Server.TraceFile != "" {
		out.Server.TraceFile = child.Server.TraceFile
	}
	if len(child.Rules) > 0 {
		out.Rules = child.Rules
	}
	if child.RulesEngine.MaxTokenBudgetPercent != 0 {
		out.RulesEngine.MaxTokenBudgetPercent = child.RulesEngine.MaxTokenBudgetPercent
	}
	if child.RulesEngine.AutoDiscovery.Enabled != nil {
		out.RulesEngine.AutoDiscovery.Enabled = child.RulesEngine.AutoDiscovery.Enabled
	}
	if len(child.RulesEngine.AutoDiscovery.FallbackOrder) > 0 {
		out.RulesEngine.AutoDiscovery.FallbackOrder = child.RulesEngine.AutoDiscovery.FallbackOrder
	}
	if len(child.Indexer.Include) > 0 {
		out.Indexer.Include = prefixPatterns(child.Indexer.Include, gitRoot, childRoot)
	}
	out.Indexer.Exclude = unionStrings(parent.Indexer.Exclude, child.Indexer.Exclude)
	out.Indexer.Parsers = mergeParsers(parent.Indexer.Parsers, child.Indexer.Parsers)
	if child.Indexer.ParseTimeoutMS != 0 {
		out.Indexer.ParseTimeoutMS = child.Indexer.ParseTimeoutMS
	}
	if child.Indexer.Concurrency != 0 {
		out.Indexer.Concurrency = child.Indexer.Concurrency
	}
	if child.Indexer.DebounceMS != 0 {
		out.Indexer.DebounceMS = child.Indexer.DebounceMS
	}
	return out
}

func prefixPatterns(patterns []string, gitRoot, childRoot string) []string {
	if gitRoot == "" || childRoot == "" {
		return patterns
	}
	rel, err := filepath.Rel(gitRoot, childRoot)
	if err != nil || rel == "." {
		return patterns
	}
	prefix := strings.Trim(filepath.ToSlash(rel), "/")
	out := make([]string, len(patterns))
	for i, pattern := range patterns {
		out[i] = prefix + "/" + strings.TrimPrefix(pattern, "/")
	}
	return out
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range append(append([]string{}, a...), b...) {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func mergeParsers(parent, child []ParserConfig) []ParserConfig {
	if len(child) == 0 {
		return parent
	}
	order := []string{}
	byExt := map[string]ParserConfig{}
	add := func(list []ParserConfig) {
		for _, item := range list {
			for _, ext := range item.Extensions {
				key := NormalizeExt(ext)
				if key == "" || key == "." {
					key = ext
				}
				if _, ok := byExt[key]; !ok {
					order = append(order, key)
				}
				cp := item
				cp.Extensions = []string{key}
				byExt[key] = cp
			}
		}
	}
	add(parent)
	add(child)
	var out []ParserConfig
	for _, ext := range order {
		out = append(out, byExt[ext])
	}
	return out
}

func ProjectRoot(configPath string) string {
	dir := filepath.Dir(configPath)
	if filepath.Base(dir) == ".onfly" {
		return filepath.Dir(dir)
	}
	return dir
}

func NearestConfig(startDir string) (string, error) {
	start, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	gitRoot, hasGit := findGitRoot(start)
	found, err := collectConfigs(start, gitRoot, hasGit)
	if err != nil {
		return "", err
	}
	return found[0], nil
}

func expand(cfg *Config) error {
	var err error
	cfg.Database.DSN, err = expandOne(cfg.Database.DSN)
	if err != nil {
		return err
	}
	cfg.Embedder.Endpoint, err = expandOne(cfg.Embedder.Endpoint)
	if err != nil {
		return err
	}
	cfg.Embedder.APIKey, err = expandOne(cfg.Embedder.APIKey)
	if err != nil {
		return err
	}
	cfg.Embedder.Model, err = expandOne(cfg.Embedder.Model)
	if err != nil {
		return err
	}
	cfg.Server.LogFile, err = expandOne(cfg.Server.LogFile)
	if err != nil {
		return err
	}
	for i, rule := range cfg.Rules {
		cfg.Rules[i], err = expandOne(rule)
		if err != nil {
			return err
		}
	}
	for i := range cfg.Indexer.Parsers {
		cfg.Indexer.Parsers[i].Path, err = expandOne(cfg.Indexer.Parsers[i].Path)
		if err != nil {
			return err
		}
		cfg.Indexer.Parsers[i].Command, err = expandOne(cfg.Indexer.Parsers[i].Command)
		if err != nil {
			return err
		}
	}
	return nil
}

func expandOne(s string) (string, error) {
	missing := ""
	out := os.Expand(s, func(key string) string {
		if key == "$" { // "$$" is a literal dollar sign
			return "$"
		}
		v, ok := os.LookupEnv(key)
		if !ok && missing == "" {
			missing = key
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("config: %s is not set", missing)
	}
	return out, nil
}

func resolve(cfg *Config) {
	root := cfg.ProjectRoot
	if cfg.Database.Driver == "sqlite" {
		cfg.Database.DSN = resolvePath(root, cfg.Database.DSN)
	}
	cfg.Server.LogFile = resolvePath(root, cfg.Server.LogFile)
	cfg.Server.TraceFile = resolvePath(root, cfg.Server.TraceFile)
	for i, rule := range cfg.Rules {
		cfg.Rules[i] = resolvePath(root, rule)
	}
	for i := range cfg.Indexer.Parsers {
		cfg.Indexer.Parsers[i].Path = resolvePath(root, cfg.Indexer.Parsers[i].Path)
	}
}

func resolvePath(root, p string) string {
	if p == "" || p == ":memory:" || filepath.IsAbs(p) || strings.Contains(p, "://") {
		return p
	}
	return filepath.Join(root, p)
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Embedder.Model) == "" {
		return fmt.Errorf("embedder.model is required")
	}
	if c.Embedder.Dimensions < 1 {
		return fmt.Errorf("embedder.dimensions is required")
	}
	if c.Embedder.Dimensions > 8192 {
		return fmt.Errorf("embedder.dimensions must be at most 8192")
	}
	if c.Embedder.MaxInputTokens != 0 && c.Embedder.MaxInputTokens < 128 {
		return fmt.Errorf("embedder.max_input_tokens must be at least 128")
	}
	if c.Indexer.DebounceMS < 0 {
		return fmt.Errorf("indexer.debounce_ms must be >= 0")
	}
	if c.Indexer.Concurrency < 0 || c.Indexer.Concurrency > 32 {
		return fmt.Errorf("indexer.concurrency must be from 0 to 32")
	}
	if c.Indexer.ParseTimeoutMS < 0 {
		return fmt.Errorf("indexer.parse_timeout_ms must be >= 0")
	}
	if c.Server.MaxTokens < 0 {
		return fmt.Errorf("server.max_tokens must be >= 0")
	}
	if c.RulesEngine.MaxTokenBudgetPercent < 1 || c.RulesEngine.MaxTokenBudgetPercent > 100 {
		return fmt.Errorf("rules_engine.max_token_budget_percent must be from 1 to 100")
	}
	for _, item := range c.RulesEngine.AutoDiscovery.FallbackOrder {
		clean := filepath.Clean(item)
		if item == "" || filepath.IsAbs(item) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("rules_engine.auto_discovery.fallback_order entry %q is invalid", item)
		}
	}
	if len(c.Indexer.Include) == 0 {
		return fmt.Errorf("indexer.include is required")
	}
	switch c.Database.Driver {
	case "sqlite", "postgres":
	default:
		return fmt.Errorf("database.driver %q is not available", c.Database.Driver)
	}
	if c.Database.DSN == "" {
		return fmt.Errorf("database.dsn is required")
	}
	switch c.Embedder.Provider {
	case "ollama", "openai", "voyage":
	default:
		return fmt.Errorf("embedder.provider %q is not available", c.Embedder.Provider)
	}
	if c.Server.Transport != "stdio" {
		return fmt.Errorf("server.transport %q is not available", c.Server.Transport)
	}
	if c.Server.LogFile == "" {
		return fmt.Errorf("server.log_file is required")
	}
	seenExt := map[string]bool{}
	for _, p := range c.Indexer.Parsers {
		if len(p.Extensions) == 0 {
			return fmt.Errorf("parser extensions are required")
		}
		for _, ext := range p.Extensions {
			ext = NormalizeExt(ext)
			if ext == "" || ext == "." {
				return fmt.Errorf("parser extension %q is invalid", ext)
			}
			if seenExt[ext] {
				return fmt.Errorf("duplicate parser extension %s", ext)
			}
			seenExt[ext] = true
		}
		switch p.Type {
		case "wasm":
			if p.Path == "" {
				return fmt.Errorf("wasm parser path is required")
			}
		case "stdio":
			if p.Command == "" {
				return fmt.Errorf("stdio parser command is required")
			}
		default:
			return fmt.Errorf("parser type %q is not available", p.Type)
		}
	}
	return nil
}
