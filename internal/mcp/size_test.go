package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/tokens"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolsTokenBudget = 1200

func TestToolDefinitionsStayWithinBudget(t *testing.T) {
	session := openServer(t, config.Config{}, seedUsers(t), fakeEmbedder{})
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, tool := range res.Tools {
		raw, _ := json.Marshal(tool)
		n, _ := tokens.Count(string(raw))
		total += n
		t.Logf("  %-18s %d", tool.Name, n)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s must be marked read-only", tool.Name)
		}
		if tool.OutputSchema != nil {
			t.Errorf("%s still sends an output schema", tool.Name)
		}
	}
	t.Logf("tool definitions: %d tokens", total)
	if total > toolsTokenBudget {
		t.Fatalf("tool definitions cost %d tokens, budget %d", total, toolsTokenBudget)
	}
}

func TestResultIsOneTextBlock(t *testing.T) {
	session := openServer(t, config.Config{}, seedUsers(t), fakeEmbedder{})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "search", Arguments: map[string]any{"query": "User", "workspace": "*"}})
	if err != nil || res.IsError || res.StructuredContent != nil || len(res.Content) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	var body struct{ Hits []hitDTO }
	if err := json.Unmarshal([]byte(res.Content[0].(*sdkmcp.TextContent).Text), &body); err != nil || len(body.Hits) == 0 {
		t.Fatalf("the text block must be the JSON result: %v", err)
	}
}

func TestFileToolsAreOptIn(t *testing.T) {
	names := func(cfg config.Config) map[string]bool {
		session := openServer(t, cfg, seedUsers(t), fakeEmbedder{})
		res, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, tool := range res.Tools {
			m[tool.Name] = true
		}
		return m
	}
	off := names(config.Config{})
	if off["read_file_raw"] || off["list_directory"] {
		t.Fatalf("file tools must be off by default: %v", off)
	}
	on := names(config.Config{Server: config.Server{FileTools: true}})
	if !on["read_file_raw"] || !on["list_directory"] {
		t.Fatalf("server.file_tools turns them on: %v", on)
	}
}
