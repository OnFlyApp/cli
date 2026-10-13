package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/axitdev/onfly/internal/config"
)

type Router struct {
	byExt   map[string]Parser
	parsers []Parser
}

type opener func(config.ParserConfig) (Parser, error)

func NewRouter(index config.Indexer, projectRoot string) (*Router, error) {
	return newRouter(index, projectRoot, openParser)
}

func newRouter(index config.Indexer, projectRoot string, open opener) (*Router, error) {
	r := &Router{byExt: map[string]Parser{}}
	for _, cfg := range index.Parsers {
		if cfg.Type == "wasm" && cfg.Path != "" && !filepath.IsAbs(cfg.Path) {
			cfg.Path = filepath.Join(projectRoot, cfg.Path)
		}
		p, err := open(cfg)
		if err != nil {
			_ = r.Close()
			return nil, err
		}
		r.parsers = append(r.parsers, p)
		for _, ext := range cfg.Extensions {
			ext = normalizeExt(ext)
			if ext == "." || ext == "" {
				_ = r.Close()
				return nil, fmt.Errorf("parser extension %q is invalid", ext)
			}
			if _, ok := r.byExt[ext]; ok {
				_ = r.Close()
				return nil, fmt.Errorf("duplicate parser extension %s", ext)
			}
			r.byExt[ext] = p
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
		return newStdioParser(cfg.Command), nil
	default:
		return nil, fmt.Errorf("parser type %q is not available", cfg.Type)
	}
}

func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

func (r *Router) match(path string) Parser {
	low := strings.ToLower(path)
	var best Parser
	bestLen := 0
	for ext, p := range r.byExt {
		if strings.HasSuffix(low, ext) && len(ext) > bestLen {
			best = p
			bestLen = len(ext)
		}
	}
	return best
}

func (r *Router) HasParser(path string) bool {
	return r.match(path) != nil
}

func (r *Router) Parse(path string, content []byte) ([]Chunk, error) {
	p := r.match(path)
	if p == nil {
		return nil, fmt.Errorf("no parser for %s", path)
	}
	return p.Parse(path, content)
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
