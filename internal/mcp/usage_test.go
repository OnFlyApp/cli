package mcp

import (
	"path/filepath"
	"testing"

	"github.com/axitdev/onfly/internal/usage"
)

func TestTrackUsageRecordsOpensThatFollowASearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	h := &tools{usage: usage.New(path)}
	h.trackUsage("search", semanticIn{Query: "where is prune"}, []hitDTO{
		{FilePath: "a.go", Symbol: "other"}, {FilePath: "internal/index/index.go", Symbol: "prune"},
	})
	h.trackUsage("get_component_ast", componentIn{Name: "prune"}, nil)
	h.trackUsage("read_file_raw", pathIn{Path: "a.go"}, nil)
	h.trackUsage("read_file_raw", pathIn{Path: "unrelated.go"}, nil)
	ev, err := usage.Load(path)
	if err != nil || len(ev) != 2 || ev[0].Rank != 2 || ev[0].Query != "where is prune" || ev[1].File != "a.go" {
		t.Fatalf("%+v %v", ev, err)
	}
	var off *tools = &tools{}
	off.trackUsage("search", semanticIn{Query: "q"}, nil)
}
