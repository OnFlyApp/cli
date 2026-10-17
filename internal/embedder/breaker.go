package embedder

import (
	"context"
	"sync"
	"time"
)

// WithBreaker makes an embedder that has just failed fail again at once for
// cooldown, so a daemon that is not running costs one slow attempt, not one per
// chunk or per query.
func WithBreaker(e Embedder, cooldown time.Duration) Embedder {
	return &breaker{Embedder: e, cooldown: cooldown}
}

type breaker struct {
	Embedder
	cooldown time.Duration
	mu       sync.Mutex
	until    time.Time
	err      error
}

func (b *breaker) tripped() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Now().Before(b.until) {
		return b.err
	}
	return nil
}

func (b *breaker) note(ctx context.Context, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	b.until, b.err = time.Now().Add(b.cooldown), err
	b.mu.Unlock()
}

func (b *breaker) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := b.tripped(); err != nil {
		return nil, err
	}
	v, err := b.Embedder.Embed(ctx, text)
	b.note(ctx, err)
	return v, err
}

func (b *breaker) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := b.tripped(); err != nil {
		return nil, err
	}
	v, err := b.Embedder.EmbedBatch(ctx, texts)
	b.note(ctx, err)
	return v, err
}
