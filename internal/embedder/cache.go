package embedder

import (
	"container/list"
	"context"
	"slices"
	"sync"
)

type cached struct {
	Embedder
	mu  sync.Mutex
	max int
	ll  *list.List // of *entry, front is newest
	m   map[string]*list.Element
}

type entry struct {
	text string
	vec  []float32
}

// Cached wraps e with an LRU of single-text Embed results, for repeated
// queries. EmbedBatch is not cached; indexing already reuses vectors by content.
func Cached(e Embedder, size int) Embedder {
	if size < 1 {
		return e
	}
	return &cached{Embedder: e, max: size, ll: list.New(), m: map[string]*list.Element{}}
}

func (c *cached) Embed(ctx context.Context, text string) ([]float32, error) {
	c.mu.Lock()
	if el, ok := c.m[text]; ok {
		c.ll.MoveToFront(el)
		v := slices.Clone(el.Value.(*entry).vec)
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()
	vec, err := c.Embedder.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[text]; !ok {
		c.m[text] = c.ll.PushFront(&entry{text: text, vec: slices.Clone(vec)})
		if c.ll.Len() > c.max {
			old := c.ll.Back()
			c.ll.Remove(old)
			delete(c.m, old.Value.(*entry).text)
		}
	}
	return vec, nil
}
