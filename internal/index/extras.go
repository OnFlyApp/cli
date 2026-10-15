package index

import (
	"context"
	"fmt"
	"strings"

	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/vectorstore"
)

const (
	slotSummary = "summary"
	slotAlt     = "alt"
)

// summaryVectorText is what the summary slot embeds: the summary and the
// questions, with no code. A chunk without either has no summary vector.
func summaryVectorText(c vectorstore.EmbeddedChunk) string {
	sum, q := c.Chunk.Metadata["summary"], c.Chunk.Metadata["questions"]
	switch {
	case sum != "" && q != "":
		return sum + "\n" + strings.ReplaceAll(q, " | ", "\n")
	case sum != "":
		return sum
	}
	return strings.ReplaceAll(q, " | ", "\n")
}

// addExtraVectors fills the optional vector slots of one file's chunks. A
// slot vector is reused when the chunk's primary vector was reused (same text,
// same summary) and the store still holds one for that text; the rest are
// embedded in batches. prev is the primary-vector reuse map embedWithReuse got.
func (s *Session) addExtraVectors(ctx context.Context, rel string, chunks []vectorstore.EmbeddedChunk, prev map[string][]float32) error {
	slots := map[string]func(vectorstore.EmbeddedChunk) string{}
	embs := map[string]embedder.Embedder{}
	if s.cfg.Indexer.SummaryVector && s.chat != nil {
		slots[slotSummary] = summaryVectorText
		embs[slotSummary] = s.emb
	}
	if s.alt != nil {
		with := s.cfg.Indexer.EmbedContext
		slots[slotAlt] = func(c vectorstore.EmbeddedChunk) string { return embedText(c.Chunk, with) }
		embs[slotAlt] = s.alt
	}
	dims := s.cfg.Embedder.Dimensions
	for slot, textOf := range slots {
		old, err := s.store.ExtraVectorsByFile(ctx, rel, slot)
		if err != nil {
			s.logger.Warn("extra vectors by file", "path", rel, "slot", slot, "err", err)
			old = nil
		}
		want := s.cfg.ExtraSlots()[slot]
		if slot == slotSummary {
			want = dims
		}
		var todo []int
		var texts []string
		for i := range chunks {
			text := textOf(chunks[i])
			if strings.TrimSpace(text) == "" {
				continue
			}
			content := chunks[i].Chunk.Content
			primaryReused := false
			if v, ok := prev[content]; ok && len(v) == dims {
				primaryReused = true
			}
			if v, ok := old[content]; ok && primaryReused && len(v) == want {
				chunks[i].Extra = setExtra(chunks[i].Extra, slot, v)
				continue
			}
			todo = append(todo, i)
			texts = append(texts, text)
		}
		batch := batchSize(s.opt)
		for start := 0; start < len(texts); start += batch {
			end := min(start+batch, len(texts))
			vecs, err := embs[slot].EmbedBatch(ctx, texts[start:end])
			if err != nil {
				return fmt.Errorf("%s vectors: %w", slot, err)
			}
			for j, v := range vecs {
				i := todo[start+j]
				if len(v) != want {
					return fmt.Errorf("%s vectors: got %d dimensions, want %d", slot, len(v), want)
				}
				chunks[i].Extra = setExtra(chunks[i].Extra, slot, v)
			}
		}
	}
	return nil
}

func setExtra(m map[string][]float32, slot string, v []float32) map[string][]float32 {
	if m == nil {
		m = map[string][]float32{}
	}
	m[slot] = v
	return m
}
