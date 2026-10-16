package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupMergesWithoutLosingOtherServers(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(file, []byte(`{"mcpServers":{"other":{"command":"x"}},"keep":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := mergeMCPServer(file, []string{"serve"}); err != nil {
			t.Fatal(err)
		}
	}
	var doc map[string]any
	raw, _ := os.ReadFile(file)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	servers := doc["mcpServers"].(map[string]any)
	if servers["other"] == nil || servers["onfly"] == nil || doc["keep"] == nil {
		t.Fatalf("%s", raw)
	}
}

func TestSetupRejectsBrokenJSON(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".mcp.json")
	if err := os.WriteFile(file, []byte(`{nope`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeMCPServer(file, []string{"serve"}); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("%v", err)
	}
}
