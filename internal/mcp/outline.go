package mcp

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/rules"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxOutline     = 300
	defaultSymbols = 20
	maxSymbols     = 50
)

type outlineIn struct {
	FilePath string `json:"file_path" jsonschema:"project-relative path of an indexed file"`
}

type symbolDTO struct {
	Name      string `json:"name,omitempty"`
	Type      string `json:"type"`
	Receiver  string `json:"receiver,omitempty"`
	FilePath  string `json:"file_path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Part      string `json:"part,omitempty"`
}

type outlineOut struct {
	Rules   string      `json:"rules,omitempty"`
	Symbols []symbolDTO `json:"symbols"`
	Omitted int         `json:"omitted,omitempty"`
}

func symbolOf(c parser.Chunk, withFile bool) symbolDTO {
	s := symbolDTO{
		Name: c.Metadata["name"], Type: c.Type, Receiver: c.Metadata["receiver"], Part: c.Metadata["part"],
		StartLine: lineNum(c.Metadata["start_line"]), EndLine: lineNum(c.Metadata["end_line"]),
	}
	if withFile {
		s.FilePath = c.FilePath
	}
	return s
}

func (h *tools) fileOutline(ctx context.Context, _ *sdkmcp.CallToolRequest, in outlineIn) (*sdkmcp.CallToolResult, outlineOut, error) {
	start := time.Now()
	rel := filepath.ToSlash(requestPath(h.root, in.FilePath))
	rel = path.Clean(strings.TrimPrefix(rel, "./"))
	if in.FilePath == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		h.record("get_file_outline", in, start, nil, 0)
		return nil, outlineOut{}, fmt.Errorf("file_path must be a project-relative path")
	}
	rows, err := h.store.ChunksByFiles(ctx, []string{rel})
	if err != nil {
		h.record("get_file_outline", in, start, nil, 0)
		return nil, outlineOut{}, err
	}
	if len(rows) == 0 {
		h.record("get_file_outline", in, start, nil, 0)
		return nil, outlineOut{}, fmt.Errorf("file %q is not in the index", rel)
	}
	out := outlineOut{Symbols: make([]symbolDTO, 0, len(rows))}
	for _, r := range rows {
		out.Symbols = append(out.Symbols, symbolOf(r.Chunk, false))
	}
	sort.SliceStable(out.Symbols, func(i, j int) bool { return out.Symbols[i].StartLine < out.Symbols[j].StartLine })
	if len(out.Symbols) > maxOutline {
		out.Omitted = len(out.Symbols) - maxOutline
		out.Symbols = out.Symbols[:maxOutline]
	}
	text := h.rulesText(rules.Query{Targets: []rules.Target{{Path: rel}}})
	out.Rules = text
	h.record("get_file_outline", in, start, out, len(out.Symbols))
	return contentWithRules(text, out), out, nil
}

type findIn struct {
	Pattern string `json:"pattern" jsonschema:"case-insensitive name; * and ? are wildcards, otherwise a prefix"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum definitions, from 1 to 50; default 20"`
}

// globMatcher compiles a case-insensitive pattern. Without a wildcard it is a prefix.
func globMatcher(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?is)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	if !strings.ContainsAny(pattern, "*?") {
		b.WriteString(".*")
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func (h *tools) findSymbols(ctx context.Context, _ *sdkmcp.CallToolRequest, in findIn) (*sdkmcp.CallToolResult, outlineOut, error) {
	start := time.Now()
	if strings.TrimSpace(in.Pattern) == "" {
		h.record("find", in, start, nil, 0)
		return nil, outlineOut{}, fmt.Errorf("pattern is required")
	}
	limit := in.Limit
	if limit < 1 {
		limit = defaultSymbols
	}
	limit = min(limit, maxSymbols)
	re, err := globMatcher(strings.TrimSpace(in.Pattern))
	if err != nil {
		h.record("find", in, start, nil, 0)
		return nil, outlineOut{}, err
	}
	names, err := h.store.Names(ctx)
	if err != nil {
		h.record("find", in, start, nil, 0)
		return nil, outlineOut{}, err
	}
	var hit []string
	for _, n := range names {
		if re.MatchString(n) {
			hit = append(hit, n)
		}
	}
	want := strings.ToLower(strings.TrimSpace(in.Pattern))
	sort.Slice(hit, func(i, j int) bool {
		ei, ej := strings.ToLower(hit[i]) == want, strings.ToLower(hit[j]) == want
		if ei != ej {
			return ei
		}
		if len(hit[i]) != len(hit[j]) {
			return len(hit[i]) < len(hit[j])
		}
		return hit[i] < hit[j]
	})
	out := outlineOut{Symbols: []symbolDTO{}}
	var chunks []parser.Chunk
loop:
	for _, n := range hit {
		defs, err := h.store.GetComponents(ctx, n)
		if err != nil {
			continue
		}
		for _, d := range defs {
			if len(out.Symbols) >= limit {
				out.Omitted++
				continue loop
			}
			out.Symbols = append(out.Symbols, symbolOf(d, true))
			chunks = append(chunks, d)
		}
	}
	text := h.rulesText(chunkQuery(chunks))
	out.Rules = text
	h.record("find", in, start, out, len(out.Symbols))
	return contentWithRules(text, out), out, nil
}

func (h *tools) addPrompts(srv *sdkmcp.Server) {
	prompt := func(name, title, desc, body string) {
		srv.AddPrompt(&sdkmcp.Prompt{
			Name: name, Title: title, Description: desc,
			Arguments: []*sdkmcp.PromptArgument{{Name: "name", Description: "component name from the index", Required: true}},
		}, func(_ context.Context, req *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
			n := strings.TrimSpace(req.Params.Arguments["name"])
			if n == "" {
				return nil, fmt.Errorf("argument name is required")
			}
			return &sdkmcp.GetPromptResult{
				Description: desc,
				Messages: []*sdkmcp.PromptMessage{{
					Role:    "user",
					Content: &sdkmcp.TextContent{Text: strings.ReplaceAll(body, "{name}", n)},
				}},
			}, nil
		})
	}
	prompt("explain-component", "Explain a component",
		"Explain what a component does, what it calls, and where it is used.",
		"Explain `{name}`. Use find (kind symbol) if the name may be ambiguous, then get_component_ast for its code, "+
			"graph (direction callees) for what it depends on, and graph (direction references) for where it is used. "+
			"Say what it does, its inputs and outputs, and anything surprising. Cite file paths and line numbers.")
	prompt("review-impact", "Review the impact of changing a component",
		"List what could break when a component changes.",
		"I am about to change `{name}`. Use get_component_ast for its current code, then graph (direction callers) with depth 3 "+
			"to find direct and indirect callers. Group the callers by file, flag the ones most likely to break, "+
			"and say which tests to run. If several definitions share the name, ask which one I mean.")
}
