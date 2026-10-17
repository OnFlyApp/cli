package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const DefaultMaxTokens = 8192

type Config struct {
	Project     string      `yaml:"project"`
	Workspace   string      `yaml:"workspace"`
	Database    Database    `yaml:"database"`
	Embedder    Embedder    `yaml:"embedder"`
	Reranker    Reranker    `yaml:"reranker"`
	Search      Search      `yaml:"search"`
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
	Provider       string    `yaml:"provider"`
	Endpoint       string    `yaml:"endpoint"`
	Model          string    `yaml:"model"`
	Dimensions     int       `yaml:"dimensions"`
	APIKey         string    `yaml:"api_key"`
	MaxInputTokens int       `yaml:"max_input_tokens"`
	QueryPrefix    string    `yaml:"query_prefix"`
	DocumentPrefix string    `yaml:"document_prefix"`
	SendDimensions *bool     `yaml:"send_dimensions"`
	Alt            *Embedder `yaml:"alt"`
}

type Reranker struct {
	Provider   string `yaml:"provider"`
	Endpoint   string `yaml:"endpoint"`
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	Candidates int    `yaml:"candidates"`
	TimeoutMS  int    `yaml:"timeout_ms"`
}

type Search struct {
	SemanticWeight    float64            `yaml:"semantic_weight"`
	KeywordWeight     float64            `yaml:"keyword_weight"`
	WordWeight        *float64           `yaml:"word_weight"`
	FieldWeight       *float64           `yaml:"field_weight"`
	RRFK              int                `yaml:"rrf_k"`
	SectionWeight     *float64           `yaml:"section_weight"`
	TypeWeights       map[string]float64 `yaml:"type_weights"`
	AltWeight         *float64           `yaml:"alt_weight"`
	SummaryWordWeight *float64           `yaml:"summary_word_weight"`
}

func (c Config) ExtraSlots() map[string]int {
	out := map[string]int{}
	if c.Embedder.Alt != nil {
		out["alt"] = c.Embedder.Alt.Dimensions
	}
	return out
}

type LLM struct {
	Provider  string `yaml:"provider"`
	Endpoint  string `yaml:"endpoint"`
	Model     string `yaml:"model"`
	APIKey    string `yaml:"api_key"`
	TimeoutMS int    `yaml:"timeout_ms"`
}

func (l LLM) Timeout() time.Duration {
	if l.TimeoutMS > 0 {
		return time.Duration(l.TimeoutMS) * time.Millisecond
	}
	return 0
}

func (r Reranker) Timeout() time.Duration {
	if r.TimeoutMS > 0 {
		return time.Duration(r.TimeoutMS) * time.Millisecond
	}
	return 0
}

type Indexer struct {
	Include        []string       `yaml:"include"`
	Exclude        []string       `yaml:"exclude"`
	Parsers        []ParserConfig `yaml:"parsers"`
	DebounceMS     int            `yaml:"debounce_ms"`
	Concurrency    int            `yaml:"concurrency"`
	EmbedContext   bool           `yaml:"embed_context"`
	ParseTimeoutMS int            `yaml:"parse_timeout_ms"`
	SkipTypes      []string       `yaml:"skip_types"`
	// IndexGenerated keeps machine-generated files (Go "Code generated ... DO NOT
	// EDIT", protobuf stubs, zz_generated) in the index; by default they are left out.
	IndexGenerated bool      `yaml:"index_generated"`
	Summaries      Summaries `yaml:"summaries"`
}

type Summaries struct {
	LLM         `yaml:",inline"`
	Concurrency int    `yaml:"concurrency"`
	Cache       string `yaml:"cache"`
}

type ParserConfig struct {
	Extensions []string `yaml:"extensions"`
	Type       string   `yaml:"type"`
	Path       string   `yaml:"path"`
	Command    string   `yaml:"command"`
	Include    []string `yaml:"include"`
	Exclude    []string `yaml:"exclude"`
}

type Server struct {
	Transport string `yaml:"transport"`
	LogLevel  string `yaml:"log_level"`
	LogFile   string `yaml:"log_file"`
	MaxTokens int    `yaml:"max_tokens"`
	TraceFile string `yaml:"trace_file"`
	UsageFile string `yaml:"usage_file"`
	Watch     bool   `yaml:"watch"`
	FileTools bool   `yaml:"file_tools"`
	// AtomicTools registers the single-purpose tools (search, find, graph, ...)
	// beside onfly_xray and onfly_focus.
	AtomicTools bool `yaml:"atomic_tools"`
	// Tools names the optional macro-tools to register: onfly_env, onfly_run.
	Tools []string `yaml:"tools"`
}

// HasTool reports whether an optional tool is switched on.
func (s Server) HasTool(name string) bool {
	for _, t := range s.Tools {
		if t == name {
			return true
		}
	}
	return false
}

// TelemetryPath is the file that records the bytes each tool call saved.
func (s Server) TelemetryPath() string {
	if s.LogFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.LogFile), "telemetry.db")
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
	applySLMEnv(&cfg)
	ApplyDefaults(&cfg)
	resolve(&cfg)
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applySLMEnv turns on index-time summaries from the environment when the
// config does not configure them: ONFLY_SLM_URL and ONFLY_SLM_MODEL name a local
// server (Ollama, llama-server, mlx_lm.server, LM Studio, vLLM), and
// ONFLY_SLM_PROVIDER picks ollama or openai (the default, except for port
// 11434). Nothing is enabled unless both URL and model are set; a server that
// is down only skips the summaries.
func applySLMEnv(cfg *Config) {
	if cfg.Indexer.Summaries.Provider != "" {
		return
	}
	url, model := strings.TrimSpace(os.Getenv("ONFLY_SLM_URL")), strings.TrimSpace(os.Getenv("ONFLY_SLM_MODEL"))
	if url == "" || model == "" {
		return
	}
	provider := strings.TrimSpace(os.Getenv("ONFLY_SLM_PROVIDER"))
	if provider == "" {
		provider = "openai"
		if strings.Contains(url, ":11434") {
			provider = "ollama"
		}
	}
	cfg.Indexer.Summaries.LLM = LLM{Provider: provider, Endpoint: url, Model: model}
}

func ApplyDefaults(cfg *Config) {
	if cfg.Embedder.Alt != nil && cfg.Embedder.Alt.MaxInputTokens == 0 {
		alt := *cfg.Embedder.Alt
		alt.MaxInputTokens = cfg.Embedder.MaxInputTokens
		if alt.MaxInputTokens == 0 {
			alt.MaxInputTokens = 6000
		}
		cfg.Embedder.Alt = &alt
	}
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
	if child.Reranker.Provider != "" {
		out.Reranker.Provider = child.Reranker.Provider
	}
	if child.Reranker.Endpoint != "" {
		out.Reranker.Endpoint = child.Reranker.Endpoint
	}
	if child.Reranker.Model != "" {
		out.Reranker.Model = child.Reranker.Model
	}
	if child.Reranker.APIKey != "" {
		out.Reranker.APIKey = child.Reranker.APIKey
	}
	if child.Reranker.Candidates != 0 {
		out.Reranker.Candidates = child.Reranker.Candidates
	}
	if child.Reranker.TimeoutMS != 0 {
		out.Reranker.TimeoutMS = child.Reranker.TimeoutMS
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
	if child.Embedder.SendDimensions != nil {
		out.Embedder.SendDimensions = child.Embedder.SendDimensions
	}
	if child.Embedder.QueryPrefix != "" {
		out.Embedder.QueryPrefix = child.Embedder.QueryPrefix
	}
	if child.Embedder.DocumentPrefix != "" {
		out.Embedder.DocumentPrefix = child.Embedder.DocumentPrefix
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
	if child.Server.Watch {
		out.Server.Watch = true
	}
	if child.Server.FileTools {
		out.Server.FileTools = true
	}
	if child.Server.AtomicTools {
		out.Server.AtomicTools = true
	}
	if child.Server.UsageFile != "" {
		out.Server.UsageFile = child.Server.UsageFile
	}
	if len(child.Search.TypeWeights) > 0 {
		out.Search.TypeWeights = child.Search.TypeWeights
	}
	if child.Search.SectionWeight != nil {
		out.Search.SectionWeight = child.Search.SectionWeight
	}
	if child.Search.AltWeight != nil {
		out.Search.AltWeight = child.Search.AltWeight
	}
	if child.Search.SummaryWordWeight != nil {
		out.Search.SummaryWordWeight = child.Search.SummaryWordWeight
	}
	if child.Embedder.Alt != nil {
		out.Embedder.Alt = child.Embedder.Alt
	}
	if child.Indexer.Summaries.Concurrency != 0 {
		out.Indexer.Summaries.Concurrency = child.Indexer.Summaries.Concurrency
	}
	if child.Indexer.Summaries.Cache != "" {
		out.Indexer.Summaries.Cache = child.Indexer.Summaries.Cache
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
	childParsers := make([]ParserConfig, len(child.Indexer.Parsers))
	for i, p := range child.Indexer.Parsers {
		p.Include = prefixPatterns(p.Include, gitRoot, childRoot)
		p.Exclude = prefixPatterns(p.Exclude, gitRoot, childRoot)
		childParsers[i] = p
	}
	out.Indexer.Parsers = mergeParsers(parent.Indexer.Parsers, childParsers)
	if child.Indexer.EmbedContext {
		out.Indexer.EmbedContext = true
	}
	if child.Search.SemanticWeight != 0 {
		out.Search.SemanticWeight = child.Search.SemanticWeight
	}
	if child.Search.KeywordWeight != 0 {
		out.Search.KeywordWeight = child.Search.KeywordWeight
	}
	if child.Search.WordWeight != nil {
		out.Search.WordWeight = child.Search.WordWeight
	}
	if child.Search.FieldWeight != nil {
		out.Search.FieldWeight = child.Search.FieldWeight
	}
	if child.Search.RRFK != 0 {
		out.Search.RRFK = child.Search.RRFK
	}
	if child.Indexer.Summaries.Provider != "" {
		out.Indexer.Summaries = child.Indexer.Summaries
	}
	if len(child.Indexer.SkipTypes) > 0 {
		out.Indexer.SkipTypes = child.Indexer.SkipTypes
	}
	if child.Indexer.ParseTimeoutMS != 0 {
		out.Indexer.ParseTimeoutMS = child.Indexer.ParseTimeoutMS
	}
	if child.Indexer.IndexGenerated {
		out.Indexer.IndexGenerated = true
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
				key += "\x00" + strings.Join(item.Include, "\x00")
				if _, ok := byExt[key]; !ok {
					order = append(order, key)
				}
				cp := item
				cp.Extensions = []string{NormalizeExt(ext)}
				if cp.Extensions[0] == "" || cp.Extensions[0] == "." {
					cp.Extensions[0] = ext
				}
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
	if a := cfg.Embedder.Alt; a != nil {
		alt := *a
		for _, f := range []*string{&alt.Endpoint, &alt.APIKey, &alt.Model} {
			if *f, err = expandOne(*f); err != nil {
				return err
			}
		}
		cfg.Embedder.Alt = &alt
	}
	for _, l := range []*LLM{&cfg.Indexer.Summaries.LLM} {
		for _, f := range []*string{&l.Endpoint, &l.APIKey, &l.Model} {
			if *f, err = expandOne(*f); err != nil {
				return err
			}
		}
	}
	cfg.Reranker.Endpoint, err = expandOne(cfg.Reranker.Endpoint)
	if err != nil {
		return err
	}
	cfg.Reranker.APIKey, err = expandOne(cfg.Reranker.APIKey)
	if err != nil {
		return err
	}
	cfg.Reranker.Model, err = expandOne(cfg.Reranker.Model)
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
		if key == "$" {
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
	cfg.Server.UsageFile = resolvePath(root, cfg.Server.UsageFile)
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
	switch c.Reranker.Provider {
	case "", "voyage", "cohere", "jina":
	default:
		return fmt.Errorf("reranker.provider %q is not available (voyage, cohere, or jina)", c.Reranker.Provider)
	}
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
	if w := c.Search; w.SemanticWeight < 0 || w.KeywordWeight < 0 {
		return fmt.Errorf("search weights must be >= 0")
	}
	if w := c.Search; (w.WordWeight != nil && *w.WordWeight < 0) || (w.FieldWeight != nil && *w.FieldWeight < 0) || w.RRFK < 0 {
		return fmt.Errorf("search.word_weight, search.field_weight and search.rrf_k must be >= 0")
	}
	for name, l := range map[string]LLM{"indexer.summaries": c.Indexer.Summaries.LLM} {
		if l.TimeoutMS < 0 {
			return fmt.Errorf("%s.timeout_ms must be >= 0", name)
		}
		if l.Provider != "" && l.Provider != "ollama" && l.Provider != "openai" {
			return fmt.Errorf("%s.provider %q is not available (use ollama or openai)", name, l.Provider)
		}
		if l.Provider != "" && l.Model == "" {
			return fmt.Errorf("%s.model is required when a provider is set", name)
		}
	}
	if c.Indexer.Summaries.Concurrency < 0 || c.Indexer.Summaries.Concurrency > 64 {
		return fmt.Errorf("indexer.summaries.concurrency must be from 0 to 64")
	}
	for typ, w := range c.Search.TypeWeights {
		if w < 0 {
			return fmt.Errorf("search.type_weights.%s must be >= 0", typ)
		}
	}
	for name, w := range map[string]*float64{"alt_weight": c.Search.AltWeight, "summary_word_weight": c.Search.SummaryWordWeight, "section_weight": c.Search.SectionWeight} {
		if w != nil && *w < 0 {
			return fmt.Errorf("search.%s must be >= 0", name)
		}
	}
	if a := c.Embedder.Alt; a != nil {
		if a.Provider == "" || strings.TrimSpace(a.Model) == "" || a.Dimensions < 1 || a.Dimensions > 8192 {
			return fmt.Errorf("embedder.alt needs provider, model and dimensions (1 to 8192)")
		}
		if a.Alt != nil {
			return fmt.Errorf("embedder.alt cannot have its own alt")
		}
	}
	if c.Reranker.TimeoutMS < 0 {
		return fmt.Errorf("reranker.timeout_ms must be >= 0")
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
		case "text":
		default:
			return fmt.Errorf("parser type %q is not available", p.Type)
		}
	}
	return nil
}
