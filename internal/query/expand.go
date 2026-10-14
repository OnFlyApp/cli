package query

import (
	"container/list"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/axitdev/onfly/internal/parser"
)

const (
	defaultExpandTimeout = 3 * time.Second
	maxExpandKeywords    = 5
	expandCacheSize      = 256
)

// expansion is what a chat model adds to a query: code-style keywords and a
// short hypothetical answer (HyDE) whose wording is closer to the code.
type expansion struct {
	Keywords []string `json:"keywords"`
	Answer   string   `json:"answer"`
}

type expandCache struct {
	mu sync.Mutex
	ll *list.List
	m  map[string]*list.Element
}

type expandEntry struct {
	q string
	e *expansion
}

func (s *Searcher) cache() *expandCache {
	s.expandOnce.Do(func() { s.expandCache = &expandCache{ll: list.New(), m: map[string]*list.Element{}} })
	return s.expandCache
}

// expand returns the model's expansion of q, or nil when it is slow, fails or
// answers nothing usable; the search then runs on the plain query.
func (s *Searcher) expand(ctx context.Context, q string) *expansion {
	c := s.cache()
	c.mu.Lock()
	if el, ok := c.m[q]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*expandEntry).e
		c.mu.Unlock()
		return e
	}
	c.mu.Unlock()
	timeout := s.ExpandTimeout
	if timeout <= 0 {
		timeout = defaultExpandTimeout
	}
	ectx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := s.Expand.Complete(ectx, "A developer searches a codebase with this question:\n"+q+"\n\n"+
		"Reply with JSON only: {\"keywords\": [up to 5 identifier-style words likely to appear in the code, "+
		"such as function names or domain terms], \"answer\": \"two sentences describing the code that answers it\"}")
	if err != nil {
		s.logger().Warn("query expansion", "err", err)
		return nil
	}
	e := parseExpansion(out)
	if e == nil {
		s.logger().Warn("query expansion returned nothing usable", "answer", truncate(out, 120))
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[q]; !ok {
		c.m[q] = c.ll.PushFront(&expandEntry{q, e})
		if c.ll.Len() > expandCacheSize {
			old := c.ll.Back()
			c.ll.Remove(old)
			delete(c.m, old.Value.(*expandEntry).q)
		}
	}
	return e
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// parseExpansion reads the model's JSON, tolerating a code fence around it.
func parseExpansion(out string) *expansion {
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			out = out[i : j+1]
		}
	}
	var e expansion
	if json.Unmarshal([]byte(out), &e) != nil {
		return nil
	}
	var kw []string
	seen := map[string]bool{}
	for _, k := range e.Keywords {
		k = strings.TrimSpace(k)
		if len(k) < 3 || seen[strings.ToLower(k)] {
			continue
		}
		seen[strings.ToLower(k)] = true
		kw = append(kw, k)
		if len(kw) == maxExpandKeywords {
			break
		}
	}
	e.Keywords, e.Answer = kw, strings.TrimSpace(e.Answer)
	if len(e.Keywords) == 0 && e.Answer == "" {
		return nil
	}
	return &e
}

// expandLists runs the expansion: the hypothetical answer through the vector
// index, and each keyword through the keyword index like a query word.
func (s *Searcher) expandLists(ctx context.Context, e *expansion, pool int, workspace string) (sem, words []parser.Chunk) {
	if e.Answer != "" {
		if vec, err := s.Emb.Embed(ctx, e.Answer); err == nil {
			if got, err := s.Store.SearchSemantic(ctx, vec, pool, workspace); err == nil {
				sem = got
			}
		}
	}
	if len(e.Keywords) > 0 {
		words = s.wordMatches(ctx, e.Keywords, pool, workspace)
	}
	return sem, words
}
