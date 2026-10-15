package cmd

import (
	"encoding/json"
	"testing"
)

func TestMCPSnippet(t *testing.T) {
	for client, wantFile := range map[string]string{
		"claude-code": ".mcp.json",
		"cursor":      ".cursor/mcp.json",
		"vscode":      ".vscode/mcp.json",
	} {
		file, body, err := mcpSnippet(client, []string{"serve", "--config", "/p/.onfly/config.yaml"})
		if err != nil || file != wantFile {
			t.Fatalf("%s: %q %v", client, file, err)
		}
		var v map[string]map[string]map[string]any
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("%s: %v", client, err)
		}
		var entry map[string]any
		for _, servers := range v {
			entry = servers["onfly"]
		}
		args, _ := entry["args"].([]any)
		if entry["command"] != "onfly" || len(args) != 3 || args[0] != "serve" {
			t.Fatalf("%s: %+v", client, entry)
		}
	}
	if _, _, err := mcpSnippet("emacs", nil); err == nil {
		t.Fatal("unknown client must fail")
	}
}

func TestClaudeAllowSnippet(t *testing.T) {
	var v struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(claudeAllowSnippet), &v); err != nil || len(v.Permissions.Allow) != 1 || v.Permissions.Allow[0] != "mcp__onfly" {
		t.Fatalf("%+v %v", v, err)
	}
}
