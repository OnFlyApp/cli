package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/config"
	"github.com/bmatcuk/doublestar/v4"
)

// AllowStdioEnv must be "1" for stdio parsers to run. A project config can
// name any shell command, so it is opt-in per machine.
const AllowStdioEnv = "ONFLY_ALLOW_STDIO"

type Router struct {
	root    string
	entries []entry
	parsers []Parser
}

// entry is one parser with the extensions and path scope it serves.
type entry struct {
	p       Parser
	ext     string
	include []string
	exclude []string
}

func (e entry) accepts(rel string) bool {
	if len(e.include) > 0 && !globAny(e.include, rel) {
		return false
	}
	return !globAny(e.exclude, rel)
}

func globAny(patterns []string, rel string) bool {
	for _, pat := range patterns {
		if ok, err := doublestar.PathMatch(pat, rel); err == nil && ok {
			return true
		}
	}
	return false
}

type opener func(config.ParserConfig) (Parser, error)

func NewRouter(index config.Indexer, projectRoot string) (*Router, error) {
	return newRouter(index, projectRoot, openParser)
}

func newRouter(index config.Indexer, projectRoot string, open opener) (*Router, error) {
	r := &Router{root: projectRoot}
	for _, cfg := range index.Parsers {
		if cfg.Type == "wasm" && cfg.Path != "" && !filepath.IsAbs(cfg.Path) {
			cfg.Path = filepath.Join(projectRoot, cfg.Path)
		}
		p, err := open(cfg)
		if err != nil {
			_ = r.Close()
			return nil, err
		}
		if d := time.Duration(index.ParseTimeoutMS) * time.Millisecond; d > 0 {
			switch v := p.(type) {
			case *WasmParser:
				v.Timeout = d
			case *StdioParser:
				v.Timeout = d
			}
		}
		r.parsers = append(r.parsers, p)
		for _, pat := range append(append([]string(nil), cfg.Include...), cfg.Exclude...) {
			if !doublestar.ValidatePattern(pat) {
				_ = r.Close()
				return nil, fmt.Errorf("parser %v: invalid include/exclude pattern %q", cfg.Extensions, pat)
			}
		}
		for _, ext := range cfg.Extensions {
			ext = config.NormalizeExt(ext)
			if ext == "." || ext == "" {
				_ = r.Close()
				return nil, fmt.Errorf("parser extension %q is invalid", ext)
			}
			for _, other := range r.entries {
				// Two parsers may share an extension only when each is scoped by include.
				if other.ext == ext && (len(other.include) == 0 || len(cfg.Include) == 0) {
					_ = r.Close()
					return nil, fmt.Errorf("duplicate parser extension %s (give each parser an include scope to share it)", ext)
				}
			}
			r.entries = append(r.entries, entry{p: p, ext: ext, include: cfg.Include, exclude: cfg.Exclude})
		}
	}
	return r, nil
}

func openParser(cfg config.ParserConfig) (Parser, error) {
	switch cfg.Type {
	case "wasm":
		if cfg.Path == "" {
			return nil, fmt.Errorf("wasm parser path is required")
		}
		return newWasmParser(context.Background(), cfg.Path)
	case "stdio":
		if cfg.Command == "" {
			return nil, fmt.Errorf("stdio parser command is required")
		}
		if os.Getenv(AllowStdioEnv) != "1" {
			return nil, fmt.Errorf("stdio parser %q runs a shell command from the project config; set %s=1 to allow it", cfg.Command, AllowStdioEnv)
		}
		return newStdioParser(cfg.Command), nil
	case "text":
		return TextParser{}, nil
	default:
		return nil, fmt.Errorf("parser type %q is not available", cfg.Type)
	}
}

// rel turns a project path, absolute or relative, into a slash path under the root.
func (r *Router) rel(path string) string {
	if filepath.IsAbs(path) && r.root != "" {
		if rel, err := filepath.Rel(r.root, path); err == nil {
			path = rel
		}
	}
	return filepath.ToSlash(path)
}

// match picks the parser with the longest extension whose scope accepts the
// file; equal extensions go to the parser listed first.
func (r *Router) match(path string) Parser {
	rel := r.rel(path)
	low := strings.ToLower(rel)
	var best Parser
	bestLen := 0
	for _, e := range r.entries {
		if len(e.ext) > bestLen && strings.HasSuffix(low, e.ext) && e.accepts(rel) {
			best = e.p
			bestLen = len(e.ext)
		}
	}
	return best
}

func (r *Router) HasParser(path string) bool {
	return r.match(path) != nil
}

func (r *Router) Parse(ctx context.Context, path string, content []byte) ([]Chunk, error) {
	p := r.match(path)
	if p == nil {
		return nil, fmt.Errorf("no parser for %s", path)
	}
	return p.Parse(ctx, path, content)
}

func (r *Router) Close() error {
	var first error
	seen := map[Parser]bool{}
	for _, p := range r.parsers {
		if seen[p] {
			continue
		}
		seen[p] = true
		if err := p.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
