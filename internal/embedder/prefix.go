package embedder

import "context"

type prefixed struct {
	Embedder
	query, doc string
}

// WithPrefixes prepends query to every Embed text (a search query) and doc to
// every EmbedBatch text (an indexed chunk). Models trained with task prompts
// retrieve better when the prompts are present. With both empty it returns e.
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
