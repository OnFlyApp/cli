package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/runner"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxCodePerFailure = 2
	maxCodeLines      = 60
)

type runIn struct {
	Command        string `json:"command" jsonschema:"the command to run, for example go test ./internal/..., one of the allowed ones"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"stop after this many seconds; default 120, at most 600"`
}

func (h *tools) run(ctx context.Context, req *sdkmcp.CallToolRequest, in runIn) (*sdkmcp.CallToolResult, struct{}, error) {
	start := time.Now()
	res, out, err := h.runCommand(ctx, req, in)
	h.record("onfly_run", in, start, resultText(res), 0)
	return res, out, err
}

func (h *tools) runCommand(ctx context.Context, _ *sdkmcp.CallToolRequest, in runIn) (*sdkmcp.CallToolResult, struct{}, error) {
	words, err := runner.Split(in.Command)
	if err != nil {
		return nil, struct{}{}, err
	}
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout <= 0 && h.cfg.Run.TimeoutSeconds > 0 {
		timeout = time.Duration(h.cfg.Run.TimeoutSeconds) * time.Second
	}
	res, err := runner.Run(ctx, h.root, words, h.cfg.Run.Commands, timeout)
	if err != nil {
		return nil, struct{}{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<summary>%s</summary>\n", res.Summary)
	if res.TimedOut {
		b.WriteString("<note>stopped at the timeout</note>\n")
	}
	if res.Truncated {
		b.WriteString("<note>the output was longer than 8 MiB and was cut</note>\n")
	}
	if len(res.Failures) > 0 {
		b.WriteString("<failures>\n")
		for _, f := range res.Failures {
			fmt.Fprintf(&b, "<failure name=%q package=%q>\n<message>\n%s\n</message>\n", f.Name, f.Package, f.Message)
			for _, c := range h.framesToCode(ctx, f.Frames) {
				fmt.Fprintf(&b, "<code at=%q>\n%s\n</code>\n", location(c), clipLines(c.Content, maxCodeLines))
			}
			b.WriteString("</failure>\n")
		}
		b.WriteString("</failures>\n")
	}
	header := fmt.Sprintf("command=%q exit=\"%d\" duration=\"%.1fs\"", res.Command, res.Exit, res.Duration.Seconds())
	return h.finishMacro(ctx, "onfly_run", res.Command, res.Raw, header, b.String(), "", 0), struct{}{}, nil
}

// framesToCode finds, for the first project frames of a failure, the function
// that contains each line. A frame in a test file gives the test; one in code
// gives the code under test. At most maxCodePerFailure are returned.
func (h *tools) framesToCode(ctx context.Context, frames []runner.Frame) []parser.Chunk {
	var out []parser.Chunk
	seen := map[string]bool{}
	for _, f := range frames {
		if len(out) == maxCodePerFailure {
			break
		}
		rows, err := h.store.ChunksByFiles(ctx, []string{f.File})
		if err != nil {
			continue
		}
		var best *parser.Chunk
		bestSpan := 1 << 30
		for i := range rows {
			c := rows[i].Chunk
			if c.Type != "function" && c.Type != "method" && c.Type != "closure" {
				continue
			}
			s, e := lineNum(c.Metadata["start_line"]), lineNum(c.Metadata["end_line"])
			if s <= 0 || e < s || f.Line < s || f.Line > e || e-s >= bestSpan {
				continue
			}
			bestSpan = e - s
			best = &rows[i].Chunk
		}
		if best == nil {
			continue
		}
		key := best.FilePath + "|" + best.Metadata["start_line"]
		if !seen[key] {
			seen[key] = true
			out = append(out, *best)
		}
	}
	return out
}

func clipLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n// ... [%d more lines] ...", len(lines)-n)
}
