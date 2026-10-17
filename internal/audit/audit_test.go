package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/telemetry"
)

const session = `{"type":"user","message":{"content":"hi"}}
{"type":"assistant","timestamp":"2026-10-01T10:00:00Z","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/proj/internal/big.go"}},{"type":"tool_use","name":"Read","input":{"file_path":"/proj/internal/big.go","limit":40}},{"type":"tool_use","name":"Grep","input":{"pattern":"x"}}]}}
{"type":"assistant","timestamp":"2026-10-01T10:01:00Z","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/proj/internal/big.go"}},{"type":"tool_use","name":"Read","input":{"file_path":"/elsewhere/x.go"}},{"type":"tool_use","name":"mcp__onfly__onfly_xray","input":{"target":"internal/small.go"}}]}}
{"type":"assistant","timestamp":"2025-01-01T00:00:00Z","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/proj/old.go"}}]}}
not json
`

func transcriptDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadTranscriptsCountsFullReadsInsideTheProject(t *testing.T) {
	tr, err := ReadTranscripts(transcriptDir(t), "/proj", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Sessions != 1 || tr.FullReads["internal/big.go"] != 2 || tr.FullReads["old.go"] != 1 || len(tr.FullReads) != 2 {
		t.Fatalf("a read with a limit and a file outside the project do not count: %+v", tr)
	}
	if tr.Tools["Read"] != 5 || tr.SearchCalls() != 1 || tr.OnflyCalls() != 1 {
		t.Fatalf("%+v", tr.Tools)
	}
	recent, _ := ReadTranscripts(transcriptDir(t), "/proj", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if recent.FullReads["old.go"] != 0 || recent.FullReads["internal/big.go"] != 2 {
		t.Fatalf("--since drops older events: %+v", recent.FullReads)
	}
}

func TestProjectDirNamesTheFolderAfterThePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := ProjectDir("/Users/x/Work/My.App")
	if want := filepath.Join(home, ".claude", "projects", "-Users-x-Work-My-App"); got != want {
		t.Fatalf("%s, want %s", got, want)
	}
}

func TestBuildReportsSavingsLargeFilesAndTips(t *testing.T) {
	tel, err := telemetry.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	ctx := context.Background()
	tel.Record(ctx, telemetry.Event{Tool: "onfly_xray", Target: "internal/big.go", RawBytes: 40000, CompressedBytes: 16000})
	tel.Record(ctx, telemetry.Event{Tool: "onfly_xray", Target: "internal/small.go", RawBytes: 800, CompressedBytes: 900})
	tel.Record(ctx, telemetry.Event{Tool: "onfly_focus", Target: "q", RawBytes: 10000, CompressedBytes: 4000})
	tr, _ := ReadTranscripts(transcriptDir(t), "/proj", time.Time{})
	sizes := map[string]int{"internal/big.go": 40000, "internal/small.go": 800, "internal/mid.go": 12000}
	rep, err := Build(ctx, Inputs{
		Telemetry: tel, Transcript: &tr, Since: time.Time{},
		Files:  []string{"internal/big.go", "internal/small.go", "internal/mid.go"},
		SizeOf: func(p string) int { return sizes[p] },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Tools) != 2 || rep.Tools[0].Tool != "onfly_xray" || rep.Tools[0].TokensSaved != 6000 || rep.Tools[1].Requests != 1 {
		t.Fatalf("%+v", rep.Tools)
	}
	if len(rep.LargeFiles) != 2 || rep.LargeFiles[0].Path != "internal/big.go" || rep.LargeFiles[0].FullReads != 2 || rep.LargeFiles[0].XrayCalls != 1 {
		t.Fatalf("a file under the threshold is not listed: %+v", rep.LargeFiles)
	}
	if rep.FullReads != 2 || rep.ReadTokens != 2*10000 {
		t.Fatalf("%d %d", rep.FullReads, rep.ReadTokens)
	}
	text := rep.Text()
	for _, want := range []string{"onfly_xray", "read whole 2", "tip: 2 full reads", "onfly_xray saved nothing on internal/small.go"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

func TestNoDataGivesOneTip(t *testing.T) {
	rep, err := Build(context.Background(), Inputs{SizeOf: func(string) int { return 0 }})
	if err != nil || len(rep.Tips) != 1 || !strings.HasPrefix(rep.Tips[0], "No data yet") {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestAnAssistantThatNeverCalledOnflyIsTold(t *testing.T) {
	tr := Transcript{Sessions: 2, Tools: map[string]int{"Read": 30, "Grep": 12}, FullReads: map[string]int{}}
	rep, _ := Build(context.Background(), Inputs{Transcript: &tr, SizeOf: func(string) int { return 0 }})
	joined := strings.Join(rep.Tips, "\n")
	if !strings.Contains(joined, "never called an Onfly tool") || !strings.Contains(joined, "Grep and Glob were called 12 times") {
		t.Fatalf("%s", joined)
	}
}
