package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultMaxTokens is the search result budget when server.max_tokens is unset.
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
	Provider       string `yaml:"provider"`
	Endpoint       string `yaml:"endpoint"`
	Model          string `yaml:"model"`
	Dimensions     int    `yaml:"dimensions"`
	APIKey         string `yaml:"api_key"`
	MaxInputTokens int    `yaml:"max_input_tokens"`
	// QueryPrefix and DocumentPrefix are prepended to every search query and to
	// every indexed chunk. Some models are trained with task prompts, for example
	// EmbeddingGemma: "task: code retrieval | query: " and "title: none | text: ".
	// Changing DocumentPrefix needs a fresh index.
	QueryPrefix    string `yaml:"query_prefix"`
	DocumentPrefix string `yaml:"document_prefix"`
	// SendDimensions controls whether the openai provider sends "dimensions" in
	// the request (it asks a model to shorten its vectors). Unset: sent to the
	// hosted OpenAI endpoint, not to a custom one, because most local servers
	// reject it for models that cannot shorten vectors.
	SendDimensions *bool `yaml:"send_dimensions"`
	// Alt is a second embedder. Every chunk is embedded by both, and search
	// fuses the two vector lists (search.alt_weight). Only the provider,
	// endpoint, model, dimensions, api_key, prefixes and max_input_tokens of
	// Alt are read. Needs a fresh index when added.
	Alt *Embedder `yaml:"alt"`
}

type Reranker struct {
	Provider   string `yaml:"provider"`
	Endpoint   string `yaml:"endpoint"`
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	Candidates int    `yaml:"candidates"`
	// TimeoutMS bounds one rerank call; 0 uses the 5 s default.
	TimeoutMS int `yaml:"timeout_ms"`
}

// Search tunes how semantic, keyword, and recency lists are fused. A zero
// weight means 1 for the semantic and keyword lists and 0 (off) for recency.
type Search struct {
	SemanticWeight float64 `yaml:"semantic_weight"`
	KeywordWeight  float64 `yaml:"keyword_weight"`
	// RecencyWeight adds a list of candidates from files changed in the last
	// RecencyDays days (git), most recent first. 0 turns it off.
	RecencyWeight float64 `yaml:"recency_weight"`
	RecencyDays   int     `yaml:"recency_days"`
	// WordWeight and FieldWeight scale the per-word keyword list and the
	// symbol-name/path/comment list against the keyword weight. Unset takes the
	// default; 0 turns the list off.
	WordWeight  *float64 `yaml:"word_weight"`
	FieldWeight *float64 `yaml:"field_weight"`
	// RRFK is the reciprocal-rank-fusion constant; 0 means 60.
	RRFK int `yaml:"rrf_k"`
	// UsageWeight adds a list of chunks an assistant opened after earlier
	// searches (needs server.usage_file). It is off at 0.
	UsageWeight float64 `yaml:"usage_weight"`
	// AltWeight and SummaryWordWeight scale the second-embedder list
	// (embedder.alt) and the summary keyword list. Unset takes the default; 0
	// turns the list off.
	// SectionWeight multiplies the score of documentation sections: below 1
	// demotes docs against code, above 1 promotes them. Unset or 1 is neutral.
	SectionWeight     *float64 `yaml:"section_weight"`
	AltWeight         *float64 `yaml:"alt_weight"`
	SummaryWordWeight *float64 `yaml:"summary_word_weight"`
}

// ExtraSlots names the extra vector slots this configuration uses, with their
// dimensions: "alt" for embedder.alt.
func (c Config) ExtraSlots() map[string]int {
	out := map[string]int{}
	if c.Embedder.Alt != nil {
		out["alt"] = c.Embedder.Alt.Dimensions
	}
	return out
}

// LLM names a chat model used for a side task (index summaries).
// An empty provider turns the feature off.
type LLM struct {
	// Provider is ollama or openai. openai means any server that speaks the
	// OpenAI chat API: vLLM, mlx_lm.server, LM Studio, llama.cpp, Msty, or OpenAI.
	Provider string `yaml:"provider"`
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`
	// APIKey is sent as a bearer token. It is optional with a custom endpoint;
	// the hosted OpenAI endpoint also reads OPENAI_API_KEY.
	APIKey    string `yaml:"api_key"`
	TimeoutMS int    `yaml:"timeout_ms"`
}

// Timeout is the bound for one call; zero lets the caller pick its default.
func (l LLM) Timeout() time.Duration {
	if l.TimeoutMS > 0 {
		return time.Duration(l.TimeoutMS) * time.Millisecond
	}
	return 0
}

// Timeout is the bound for one rerank call. An LLM reranker is slower than a
// rerank endpoint, so ollama defaults to 30 s; zero elsewhere means the 5 s default.
func (r Reranker) Timeout() time.Duration {
	if r.TimeoutMS > 0 {
		return time.Duration(r.TimeoutMS) * time.Millisecond
	}
	if r.Provider == "ollama" || r.Provider == "ollama-score" || r.Provider == "openai-score" {
		return 30 * time.Second
	}
	return 0
}

type Indexer struct {
	Include    []string       `yaml:"include"`
	Exclude    []string       `yaml:"exclude"`
	Parsers    []ParserConfig `yaml:"parsers"`
	DebounceMS int            `yaml:"debounce_ms"`
	// Concurrency is the number of files indexed in parallel (0 or 1: sequential).
	Concurrency int `yaml:"concurrency"`
	// EmbedContext prefixes each chunk's embedded text with its path and symbol.
	// Changing it only affects chunks embedded afterwards; reindex to apply it everywhere.
	EmbedContext bool `yaml:"embed_context"`
	// ParseTimeoutMS bounds one parser call; 0 uses the 5 s default.
	ParseTimeoutMS int `yaml:"parse_timeout_ms"`
	// SkipTypes drops chunks of these types (case-insensitive) after parsing,
	// for example "use" or "import" statements that crowd out real code.
	// "onfly stats" lists the types an index holds. Reindex to apply it.
	SkipTypes []string `yaml:"skip_types"`
	// Summaries has a chat model write a one-sentence description of each named
	// chunk; it is embedded and searched with the code. Off when provider is empty.
	Summaries Summaries `yaml:"summaries"`
}

// Summaries is an LLM plus how it is used while indexing.
type Summaries struct {
	LLM `yaml:",inline"`
	// Files also writes one chunk per file (type "file") that lists its symbols
	// and carries a summary, so a question about a package finds the file.
	Files bool `yaml:"files"`
	// Context adds to each summary prompt which symbols of the same file the
	// chunk calls, is called by, and sits next to. A different prompt means new
	// cache keys, so turning it on asks the model again.
	Context bool `yaml:"context"`
	// Concurrency is how many chunks of one file are sent to the model at once
	// (1 when 0). Servers that batch requests, such as vLLM and mlx_lm.server,
	// answer many in the time of one; it multiplies with indexer.concurrency.
	Concurrency int `yaml:"concurrency"`
	// Cache is a JSON-lines file of model answers keyed by chunk text, reused by
	// later indexes. Empty means .onfly/summaries.jsonl; "none" turns it off.
	Cache string `yaml:"cache"`
}

type ParserConfig struct {
	Extensions []string `yaml:"extensions"`
	Type       string   `yaml:"type"`
	Path       string   `yaml:"path"`
	Command    string   `yaml:"command"`
	// Include and Exclude narrow this parser to a part of the project, as
	// doublestar globs relative to the project root. Empty Include means
	// every file with a matching extension. A file must also pass indexer.include.
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

type Server struct {
	Transport string `yaml:"transport"`
	LogLevel  string `yaml:"log_level"`
	LogFile   string `yaml:"log_file"`
	MaxTokens int    `yaml:"max_tokens"`
	TraceFile string `yaml:"trace_file"`
	// UsageFile, when set, appends one JSON line each time an assistant opens a
	// chunk that the latest search returned. `onfly eval mine` turns it into eval cases.
	UsageFile string `yaml:"usage_file"`
	Watch     bool   `yaml:"watch"`
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
	if child.Server.UsageFile != "" {
		out.Server.UsageFile = child.Server.UsageFile
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
	if child.Search.UsageWeight != 0 {
		out.Search.UsageWeight = child.Search.UsageWeight
	}
	if child.Indexer.Summaries.Files {
		out.Indexer.Summaries.Files = true
	}
	if child.Indexer.Summaries.Concurrency != 0 {
		out.Indexer.Summaries.Concurrency = child.Indexer.Summaries.Concurrency
	}
	if child.Indexer.Summaries.Context {
		out.Indexer.Summaries.Context = true
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
	if child.Search.RecencyWeight != 0 {
		out.Search.RecencyWeight = child.Search.RecencyWeight
	}
	if child.Search.RecencyDays != 0 {
		out.Search.RecencyDays = child.Search.RecencyDays
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
				// The same extension may appear once per distinct scope.
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
	case "", "voyage", "cohere", "jina", "ollama", "ollama-score", "openai-score":
	default:
		return fmt.Errorf("reranker.provider %q is not available (voyage, cohere, jina, ollama, ollama-score, or openai-score)", c.Reranker.Provider)
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
	if w := c.Search; w.SemanticWeight < 0 || w.KeywordWeight < 0 || w.RecencyWeight < 0 || w.RecencyDays < 0 {
		return fmt.Errorf("search weights and recency_days must be >= 0")
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
	if c.Search.UsageWeight < 0 || c.Indexer.Summaries.Concurrency < 0 || c.Indexer.Summaries.Concurrency > 64 {
		return fmt.Errorf("search.usage_weight must be >= 0 and indexer.summaries.concurrency from 0 to 64")
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
