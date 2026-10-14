// Package usage records which search hit an assistant opens next, so real
// questions can become eval cases and opened chunks can rank a little higher.
package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Window is how long after a search an open still counts as following it.
const Window = 10 * time.Minute

// Hit is one search result, reduced to what an open can be matched against.
type Hit struct{ File, Symbol string }

// Event is one open that followed a search: the query, the hit that was
// opened, and its 1-based position in that search's results.
type Event struct {
	TS     string `json:"ts"`
	Query  string `json:"query"`
	File   string `json:"file"`
	Symbol string `json:"symbol,omitempty"`
	Rank   int    `json:"rank"`
}

// Log appends events to a JSON-lines file. A nil *Log records nothing.
type Log struct {
	path string
	now  func() time.Time

	mu    sync.Mutex
	query string
	hits  []Hit
	used  []bool
	at    time.Time
}

// New returns a Log writing to path, or nil when path is empty.
func New(path string) *Log {
	if path == "" {
		return nil
	}
	return &Log{path: path, now: time.Now}
}

// Search remembers the results of the latest search.
func (l *Log) Search(query string, hits []Hit) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.query, l.hits, l.used, l.at = query, hits, make([]bool, len(hits)), l.now()
}

// Open records that file (and symbol, when known) was opened. It is kept only
// when it matches a hit of the latest search made within Window; each hit is
// recorded once per search.
func (l *Log) Open(file, symbol string) error {
	if l == nil || (file == "" && symbol == "") {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.query == "" || l.now().Sub(l.at) > Window {
		return nil
	}
	for i, h := range l.hits {
		if l.used[i] || !matches(h, file, symbol) {
			continue
		}
		l.used[i] = true
		return l.append(Event{TS: l.now().UTC().Format(time.RFC3339), Query: l.query, File: h.File, Symbol: h.Symbol, Rank: i + 1})
	}
	return nil
}

func matches(h Hit, file, symbol string) bool {
	switch {
	case file != "":
		return h.File == file && (symbol == "" || h.Symbol == symbol)
	default:
		return h.Symbol == symbol
	}
}

func (l *Log) append(e Event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

// Load reads every event from path. A missing file is no events; a damaged
// line is skipped.
func Load(path string) ([]Event, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Query != "" && e.File != "" {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Key identifies a chunk in Counts.
func Key(file, symbol string) string { return file + "\x00" + symbol }

// Counter reads open counts from a usage file and reloads when it changes.
type Counter struct {
	Path string

	mu     sync.Mutex
	size   int64
	mod    time.Time
	checks time.Time
	counts map[string]int
}

// Counts returns how often each (file, symbol) was opened after a search.
func (c *Counter) Counts() map[string]int {
	if c == nil || c.Path == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts != nil && time.Since(c.checks) < 5*time.Second {
		return c.counts
	}
	c.checks = time.Now()
	st, err := os.Stat(c.Path)
	if err != nil {
		c.counts = map[string]int{}
		return c.counts
	}
	if c.counts != nil && st.Size() == c.size && st.ModTime().Equal(c.mod) {
		return c.counts
	}
	events, _ := Load(c.Path)
	counts := map[string]int{}
	for _, e := range events {
		counts[Key(e.File, e.Symbol)]++
	}
	c.size, c.mod, c.counts = st.Size(), st.ModTime(), counts
	return counts
}
