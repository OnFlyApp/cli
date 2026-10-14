package query

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type stubChat struct {
	out   string
	err   error
	calls int
	delay time.Duration
}

func (c *stubChat) Complete(ctx context.Context, _ string) (string, error) {
	c.calls++
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return c.out, c.err
}

func TestParseExpansion(t *testing.T) {
	e := parseExpansion("```json\n{\"keywords\": [\"retry\", \"ab\", \"Retry\", \"backoff\"], \"answer\": \" Retries requests. \"}\n```")
	if e == nil || len(e.Keywords) != 2 || e.Keywords[0] != "retry" || e.Answer != "Retries requests." {
		t.Fatalf("%+v", e)
	}
	if parseExpansion("no json here") != nil || parseExpansion(`{"keywords": [], "answer": ""}`) != nil {
		t.Fatal("unusable answers must give nil")
	}
}

func TestExpansionAddsKeywordHits(t *testing.T) {
	// The query shares no word with the target; the expansion supplies one.
	target := chunkIn("retry.go", "backoffLoop", "func backoffLoop() { sleep }")
	other := chunkIn("x.go", "other", "func other() {}")
	s := searcherWith(t, other, target)
	got, _ := s.Search(context.Background(), "wait before trying again", 5, "")
	for _, c := range got {
		if c.FilePath == "retry.go" && got[0].FilePath == "retry.go" {
			t.Skip("target already first; the plain search found it")
		}
	}
	chat := &stubChat{out: `{"keywords": ["backoffLoop"], "answer": "sleeps between attempts"}`}
	s.Expand = chat
	got, err := s.Search(context.Background(), "wait before trying again", 5, "")
	if err != nil || len(got) == 0 || got[0].FilePath != "retry.go" {
		t.Fatalf("expansion keyword must surface the chunk: %+v %v", got, err)
	}
	if _, err := s.Search(context.Background(), "wait before trying again", 5, ""); err != nil || chat.calls != 1 {
		t.Fatalf("a repeated query must come from the cache, calls=%d err=%v", chat.calls, err)
	}
}

func TestExpansionFailureAndTimeoutFallBack(t *testing.T) {
	s := searcherWith(t, chunkIn("a.go", "getUserId", "func getUserId() {}"))
	for name, chat := range map[string]*stubChat{
		"error":   {err: errors.New("down")},
		"garbage": {out: "sorry"},
		"slow":    {out: `{"keywords":["x"]}`, delay: time.Second},
	} {
		s.Expand = chat
		s.ExpandTimeout = 30 * time.Millisecond
		s.expandCache, s.expandOnce = nil, sync.Once{}
		start := time.Now()
		got, err := s.Search(context.Background(), "getUserId", 5, "")
		if err != nil || len(got) != 1 {
			t.Errorf("%s: %+v %v", name, got, err)
		}
		if time.Since(start) > 500*time.Millisecond {
			t.Errorf("%s: timeout not honoured", name)
		}
	}
}
