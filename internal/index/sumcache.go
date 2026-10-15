package index

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/axitdev/onfly/internal/parser"
)

// promptVersion is part of every cache key; bump it when the summary prompt changes.
const promptVersion = "v1"

// summaryKey identifies the model's answer for a chunk: the model, whether
// questions were requested, the chunk type and name, and its exact text.
func summaryKey(model string, questions int, c parser.Chunk) string {
	return ContentHash([]byte(promptVersion + "\x00" + model + "\x00" + strconv.Itoa(questions) + "\x00" +
		c.Type + "\x00" + c.Metadata["name"] + "\x00" + c.Content))
}

// summaryCache is an append-only JSON-lines file of model answers keyed by
// summaryKey. It survives deleting the database, so a full reindex, a new
// embedder or a new prefix does not pay for the model again. A nil cache
// answers nothing and stores nothing.
type summaryCache struct {
	path string
	mu   sync.Mutex
	m    map[string]generated
}

type cacheLine struct {
	K string   `json:"k"`
	S string   `json:"s"`
	Q []string `json:"q,omitempty"`
}

// openSummaryCache loads path. An empty path or "none" disables the cache.
func openSummaryCache(path string) *summaryCache {
	if path == "" || path == "none" {
		return nil
	}
	c := &summaryCache{path: path, m: map[string]generated{}}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		var l cacheLine
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.K != "" && l.S != "" {
			c.m[l.K] = generated{Summary: l.S, Questions: l.Q}
		}
	}
	return c
}

func (c *summaryCache) get(key string) (generated, bool) {
	if c == nil {
		return generated{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.m[key]
	return g, ok
}

func (c *summaryCache) put(key string, g generated) {
	if c == nil || g.Summary == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; ok {
		return
	}
	c.m[key] = g
	raw, err := json.Marshal(cacheLine{K: key, S: g.Summary, Q: g.Questions})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}
