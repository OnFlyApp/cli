package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Project     string   `mapstructure:"project"`
	Workspace   string   `mapstructure:"workspace"`
	Database    Database `mapstructure:"database"`
	Embedder    Embedder `mapstructure:"embedder"`
	Rules       []string `mapstructure:"rules"`
	Indexer     Indexer  `mapstructure:"indexer"`
	Server      Server   `mapstructure:"server"`
	ProjectRoot string   `mapstructure:"-"`
}

type Database struct {
	Driver string `mapstructure:"driver"`
	DSN    string `mapstructure:"dsn"`
}

type Embedder struct {
	Provider       string `mapstructure:"provider"`
	Endpoint       string `mapstructure:"endpoint"`
	Model          string `mapstructure:"model"`
	Dimensions     int    `mapstructure:"dimensions"`
	APIKey         string `mapstructure:"api_key"`
	MaxInputTokens int    `mapstructure:"max_input_tokens"`
}

type Indexer struct {
	Include    []string       `mapstructure:"include"`
	Exclude    []string       `mapstructure:"exclude"`
	Parsers    []ParserConfig `mapstructure:"parsers"`
	DebounceMS int            `mapstructure:"debounce_ms"`
}

type ParserConfig struct {
	Extensions []string `mapstructure:"extensions"`
	Type       string   `mapstructure:"type"`
	Path       string   `mapstructure:"path"`
	Command    string   `mapstructure:"command"`
}

type Server struct {
	Transport string `mapstructure:"transport"`
	LogLevel  string `mapstructure:"log_level"`
	LogFile   string `mapstructure:"log_file"`
	MaxTokens int    `mapstructure:"max_tokens"`
	TraceFile string `mapstructure:"trace_file"`
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
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, err
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
		cfg.Server.MaxTokens = 8192
	}
	if cfg.Server.TraceFile == "" {
		cfg.Server.TraceFile = ".onfly/mcp_trace.jsonl"
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
	if len(child.Indexer.Include) > 0 {
		out.Indexer.Include = prefixPatterns(child.Indexer.Include, gitRoot, childRoot)
	}
	out.Indexer.Exclude = unionStrings(parent.Indexer.Exclude, child.Indexer.Exclude)
	out.Indexer.Parsers = mergeParsers(parent.Indexer.Parsers, child.Indexer.Parsers)
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
				key := normalizeExt(ext)
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
	if c.Server.MaxTokens < 0 {
		return fmt.Errorf("server.max_tokens must be >= 0")
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
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext != "" && !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
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
