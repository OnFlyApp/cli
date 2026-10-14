// Package storetest holds fixtures for tests that need a populated store.
package storetest

import (
	"context"

	"github.com/axitdev/onfly/internal/vectorstore"
)

// Save writes chunks the way an index run would: one CommitFile per file path,
// in first-seen order, replacing any chunks the store already holds for it.
func Save(ctx context.Context, st vectorstore.Store, chunks []vectorstore.EmbeddedChunk) error {
	var order []string
	byPath := map[string][]vectorstore.EmbeddedChunk{}
	for _, c := range chunks {
		p := c.Chunk.FilePath
		if _, ok := byPath[p]; !ok {
			order = append(order, p)
		}
		byPath[p] = append(byPath[p], c)
	}
	for _, p := range order {
		if err := st.CommitFile(ctx, p, "fixture", byPath[p]); err != nil {
			return err
		}
	}
	return nil
}
