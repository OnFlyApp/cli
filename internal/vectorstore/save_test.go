package vectorstore

import "context"

func saveChunks(ctx context.Context, st Store, chunks []EmbeddedChunk) error {
	var order []string
	byPath := map[string][]EmbeddedChunk{}
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
