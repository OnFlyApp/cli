package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/axitdev/onfly/internal/gitx"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultHistory = 5
	maxHistory     = 20
)

type historyIn struct {
	Name     string `json:"name" jsonschema:"component name from chunk metadata"`
	Receiver string `json:"receiver,omitempty" jsonschema:"receiver or class, to pick one of several same-name definitions"`
	FilePath string `json:"file_path,omitempty" jsonschema:"file of the definition, to pick one of several same-name definitions"`
	Limit    int    `json:"limit,omitempty" jsonschema:"commits to return, from 1 to 20; default 5"`
}

type historyOut struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	// Scope is "symbol" when the commits touched the symbol's lines, "file"
	// when the definition has no line range and the whole file's log is shown.
	Scope   string        `json:"scope"`
	Commits []gitx.Commit `json:"commits"`
}

func (h *tools) getHistory(ctx context.Context, _ *sdkmcp.CallToolRequest, in historyIn) (*sdkmcp.CallToolResult, historyOut, error) {
	start := time.Now()
	n := in.Limit
	if n < 1 {
		n = defaultHistory
	}
	n = min(n, maxHistory)
	defs, err := h.store.GetComponents(ctx, in.Name)
	if errors.Is(err, vectorstore.ErrNotFound) {
		return nil, historyOut{}, fmt.Errorf("component %q not found", in.Name)
	}
	if err != nil {
		return nil, historyOut{}, err
	}
	def, ok := pickDefinition(defs, in.Receiver, in.FilePath)
	if !ok {
		return nil, historyOut{}, fmt.Errorf("component %q has no definition matching receiver %q file %q", in.Name, in.Receiver, in.FilePath)
	}
	out := historyOut{FilePath: def.FilePath, StartLine: lineNum(def.Metadata["start_line"]), EndLine: lineNum(def.Metadata["end_line"])}
	if out.StartLine > 0 && out.EndLine >= out.StartLine {
		out.Scope = "symbol"
		out.Commits, err = gitx.LineLog(ctx, h.root, def.FilePath, out.StartLine, out.EndLine, n)
	} else {
		out.Scope, out.StartLine, out.EndLine = "file", 0, 0
		out.Commits, err = gitx.FileLog(ctx, h.root, def.FilePath, n)
	}
	if err != nil {
		return nil, historyOut{}, fmt.Errorf("history needs a git repository with this file committed: %w", err)
	}
	h.record("get_history", in, start, out, len(out.Commits))
	return nil, out, nil
}
