package query

import (
	"context"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
)

// Lexical lists (keyword, per-word, symbol and path) get 0.65 of the weight and
// semantic ones 0.35. A question about "skeleton body collapse" names things;
// the embedder alone puts small unrelated types ahead of the code that does it.
const (
	focusLexical  = 0.65
	focusSemantic = 0.35
	tinyLines     = 4
)

func FocusParams(p Params) Params {
	p.Semantic, p.Keyword, p.Word, p.Field = focusSemantic, focusLexical, 1, 1
	if p.Alt > 0 {
		p.Alt = focusSemantic
	}
	return p
}

// RunFocus is Run with FocusParams, and with tiny declarations that the query
// does not name moved behind everything else.
func (s *Searcher) RunFocus(ctx context.Context, r Request) ([]parser.Chunk, error) {
	g, err := s.Gather(ctx, r)
	if err != nil {
		return nil, err
	}
	fused := s.rerank(ctx, r.Query, demoteBoilerplate(s.Fuse(g, FocusParams(s.Params())), g.Terms))
	if n := ClampLimit(r.Limit); len(fused) > n {
		fused = fused[:n]
	}
	return fused, nil
}

var boilerplateTypes = map[string]bool{
	"struct": true, "type": true, "interface": true, "constant": true,
	"property": true, "enum_case": true, "trait_use": true, "class": true,
}

func demoteBoilerplate(ranked []parser.Chunk, terms []string) []parser.Chunk {
	out := make([]parser.Chunk, 0, len(ranked))
	var tiny []parser.Chunk
	for _, c := range ranked {
		if boilerplateTypes[strings.ToLower(c.Type)] && strings.Count(strings.TrimSpace(c.Content), "\n") < tinyLines && !namedBy(c, terms) {
			tiny = append(tiny, c)
			continue
		}
		out = append(out, c)
	}
	return append(out, tiny...)
}

func namedBy(c parser.Chunk, terms []string) bool {
	name := strings.ToLower(c.Metadata["name"])
	if name == "" {
		return false
	}
	for _, t := range terms {
		if t = strings.ToLower(t); len(t) >= 3 && strings.Contains(name, t) {
			return true
		}
	}
	return false
}
