package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/rules"
	"github.com/axitdev/onfly/internal/telemetry"
	"github.com/axitdev/onfly/internal/tokens"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// rulesOnce returns the rules that match q and have not yet been sent in this
// session, so a rule costs its tokens once however often the files recur.
func (h *tools) rulesOnce(q rules.Query) (string, int) {
	if h.engine == nil {
		return "", 0
	}
	h.seenMu.Lock()
	defer h.seenMu.Unlock()
	if h.seen == nil {
		h.seen = map[string]bool{}
	}
	return h.engine.InjectNew(q, h.maxTokens, h.seen)
}

func (h *tools) fileSize(rel string) int {
	info, err := os.Stat(filepath.Join(h.root, filepath.FromSlash(rel)))
	if err != nil {
		return 0
	}
	return int(info.Size())
}

func (h *tools) finishMacro(ctx context.Context, tool, target string, raw int, header, body, rulesText string, nRules int) *sdkmcp.CallToolResult {
	var b strings.Builder
	b.WriteString(body)
	if rulesText != "" {
		b.WriteString("<architectural_rules>\n" + strings.TrimSpace(rulesText) + "\n</architectural_rules>\n")
	}
	b.WriteString("</onfly_context>")
	closing := b.String()
	saved := 0
	if full := len(header) + len(closing); raw > full {
		saved = (raw - full) / 4
	}
	text := fmt.Sprintf(`<onfly_context %s tokens_saved="%d">`+"\n", header, saved) + closing
	h.tel.Record(ctx, telemetry.Event{Tool: tool, Target: target, RawBytes: raw, CompressedBytes: len(text), Rules: nRules})
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}}
}

func signature(c parser.Chunk) string {
	first, _, _ := strings.Cut(strings.TrimSpace(c.Content), "\n")
	return clip(strings.TrimSpace(first), 200)
}

func location(c parser.Chunk) string {
	if l := lineNum(c.Metadata["start_line"]); l > 0 {
		return fmt.Sprintf("%s:%d", c.FilePath, l)
	}
	return c.FilePath
}

func cutToBudget(s string, maxTokens int) string {
	if n, err := tokens.Count(s); err != nil || n <= maxTokens {
		return s
	}
	limit := maxTokens * 3
	if limit >= len(s) {
		return s
	}
	cut := strings.LastIndex(s[:limit], "\n")
	if cut < 0 {
		cut = limit
	}
	return s[:cut] + "\n// ... [truncated to fit the token budget] ..."
}

func resultText(res *sdkmcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	if t, ok := res.Content[0].(*sdkmcp.TextContent); ok {
		return t.Text
	}
	return ""
}
