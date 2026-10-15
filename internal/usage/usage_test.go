package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenAfterSearchIsRecordedOncePerHit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	l := New(path)
	l.Search("where is prune", []Hit{{"a.go", "x"}, {"internal/index.go", "prune"}})
	if err := l.Open("internal/index.go", ""); err != nil {
		t.Fatal(err)
	}
	l.Open("internal/index.go", "prune")
	l.Open("other.go", "")
	l.Open("", "x")
	ev, err := Load(path)
	if err != nil || len(ev) != 2 || ev[0].Rank != 2 || ev[0].Symbol != "prune" || ev[1].File != "a.go" || ev[1].Rank != 1 {
		t.Fatalf("%+v %v", ev, err)
	}
}

func TestOpenOutsideWindowAndNilLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.jsonl")
	l := New(path)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.Search("q", []Hit{{"a.go", ""}})
	l.now = func() time.Time { return now.Add(Window + time.Minute) }
	l.Open("a.go", "")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("an open long after the search must not be recorded")
	}
	var nilLog *Log
	nilLog.Search("q", nil)
	if err := nilLog.Open("a.go", ""); err != nil || New("") != nil {
		t.Fatal("nil log is a no-op")
	}
	if ev, err := Load(filepath.Join(t.TempDir(), "missing")); err != nil || ev != nil {
		t.Fatal("missing file is no events")
	}
}
