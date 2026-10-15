package mcp

import (
	"context"
	"encoding/json"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// addTool registers a read-only tool whose result is one compact JSON text
// block. The SDK would otherwise publish an output schema in tools/list and
// send the result twice, as structuredContent and as text; both cost context
// on every session and every call.
func addTool[In, Out any](srv *sdkmcp.Server, t *sdkmcp.Tool, h func(context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, Out, error)) {
	closed := false
	t.Annotations = &sdkmcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}
	sdkmcp.AddTool(srv, t, func(ctx context.Context, req *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, any, error) {
		res, out, err := h(ctx, req, in)
		if err != nil {
			return nil, nil, err
		}
		if res != nil && (len(res.Content) > 0 || res.InputRequests != nil) {
			return res, nil, nil
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, nil, err
		}
		if res == nil {
			res = &sdkmcp.CallToolResult{}
		}
		res.Content = []sdkmcp.Content{&sdkmcp.TextContent{Text: string(raw)}}
		return res, nil, nil
	})
}
