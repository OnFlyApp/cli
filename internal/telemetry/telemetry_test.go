package telemetry

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSummarySumsEvents(t *testing.T) {
	ctx := context.Background()
	l, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Record(ctx, Event{Tool: "onfly_xray", Target: "a.go", RawBytes: 4000, CompressedBytes: 400, Rules: 1})
	l.Record(ctx, Event{Tool: "onfly_focus", Target: "q", RawBytes: 1000, CompressedBytes: 600})
	s, err := l.Summary(ctx)
	if err != nil || s.Requests != 2 || s.RawBytes != 5000 || s.CompressedBytes != 1000 || s.Rules != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	if s.Saved() != 1000 || s.Ratio() != 0.8 {
		t.Fatalf("saved %d ratio %v", s.Saved(), s.Ratio())
	}
}

func TestNilLogIsSafe(t *testing.T) {
	var l *Log
	l.Record(context.Background(), Event{})
	if s, err := l.Summary(context.Background()); err != nil || s.Requests != 0 {
		t.Fatalf("%+v %v", s, err)
	}
}
