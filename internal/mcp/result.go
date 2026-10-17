package mcp

import (
	"context"
	"encoding/json"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// addActionTool registers a tool that starts processes: it is not read-only,
// and a client that asks for confirmation of such tools will.
func addActionTool[In, Out any](srv *sdkmcp.Server, t *sdkmcp.Tool, h func(context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, Out, error)) {
	addToolWith(srv, t, false, h)
}

func addTool[In, Out any](srv *sdkmcp.Server, t *sdkmcp.Tool, h func(context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, Out, error)) {
	addToolWith(srv, t, true, h)
}

func addToolWith[In, Out any](srv *sdkmcp.Server, t *sdkmcp.Tool, readOnly bool, h func(context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, Out, error)) {
	closed := false
	t.Annotations = &sdkmcp.ToolAnnotations{ReadOnlyHint: readOnly, OpenWorldHint: &closed}
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
