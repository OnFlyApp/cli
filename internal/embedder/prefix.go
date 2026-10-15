package embedder

import "context"

type prefixed struct {
	Embedder
	query, doc string
}

func WithPrefixes(e Embedder, query, doc string) Embedder {
	if query == "" && doc == "" {
		return e
	}
	return &prefixed{Embedder: e, query: query, doc: doc}
}

func (p *prefixed) Embed(ctx context.Context, text string) ([]float32, error) {
	return p.Embedder.Embed(ctx, p.query+text)
}

func (p *prefixed) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if p.doc == "" {
		return p.Embedder.EmbedBatch(ctx, texts)
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = p.doc + t
	}
	return p.Embedder.EmbedBatch(ctx, out)
}
