package index

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/axitdev/onfly/internal/parser"
)

const promptVersion = "v1"

const legacyQuestionCount = "0"

func summaryKey(model string, c parser.Chunk) string {
	key := promptVersion + "\x00" + model + "\x00" + legacyQuestionCount + "\x00" +
		c.Type + "\x00" + c.Metadata["name"] + "\x00" + c.Content
	return ContentHash([]byte(key))
}

type summaryCache struct {
	path string
	mu   sync.Mutex
	m    map[string]generated
}

type cacheLine struct {
	K string `json:"k"`
	S string `json:"s"`
}

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
			c.m[l.K] = generated{Summary: l.S}
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
	raw, err := json.Marshal(cacheLine{K: key, S: g.Summary})
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
