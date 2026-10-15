package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/tokens"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolsTokenBudget caps what tools/list costs a session. A tool or argument
// description that pushes past it should be shortened, or the budget raised
// on purpose.
const toolsTokenBudget = 1600

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
