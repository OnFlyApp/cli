package mcp

import (
	"context"
	"os"
	"path/filepath"

	"github.com/axitdev/onfly/internal/index"
)

const (
	maxStaleFiles = 20
	maxStaleBytes = 1 << 20
)

func (h *tools) markStale(ctx context.Context, hits []hitDTO) {
	if h.store == nil || h.root == "" {
		return
	}
	stale := map[string]bool{}
	checked := map[string]bool{}
	for _, hit := range hits {
		p := hit.FilePath
		if p == "" || checked[p] {
			continue
		}
		if len(checked) >= maxStaleFiles {
			break
		}
		checked[p] = true
		indexed, ok, err := h.store.FileHash(ctx, p)
		if err != nil || !ok {
			continue
		}
		full := filepath.Join(h.root, filepath.FromSlash(p))
		info, err := os.Stat(full)
		if os.IsNotExist(err) {
			stale[p] = true
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxStaleBytes {
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		if index.ContentHash(raw) != indexed {
			stale[p] = true
		}
	}
	for i := range hits {
		if stale[hits[i].FilePath] {
			hits[i].Stale = true
		}
	}
}
