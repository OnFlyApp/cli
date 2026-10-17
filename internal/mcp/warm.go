package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/axitdev/onfly/internal/index"
)

// warm makes the index usable before a macro tool answers, without waiting for
// the embedder: an empty index is built in process, and a file that is missing
// from the index or changed on disk since it was indexed is synced alone. Both
// store chunks without new vectors, which search by name, text and path does not
// need; embedding then continues in the background. rel is "" when the target
// is not known to be a file.
func (h *tools) warm(ctx context.Context, rel string) error {
	if h.root == "" || h.store == nil {
		return nil
	}
	h.warmMu.Lock()
	defer h.warmMu.Unlock()
	files, err := h.store.ListFiles(ctx)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		h.logger.Info("index is empty, building it")
		if _, err := index.Run(ctx, h.cfg, h.logger, index.Options{Lexical: true}); err != nil {
			return fmt.Errorf("the index is empty and building it failed: %w", err)
		}
		h.embedInBackground()
		return nil
	}
	if rel == "" {
		return nil
	}
	return h.syncFile(ctx, rel, files)
}

func (h *tools) syncFile(ctx context.Context, rel string, files []string) error {
	full := filepath.Join(h.root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStaleBytes {
		return nil
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	indexed, ok, err := h.store.FileHash(ctx, rel)
	if err != nil {
		return err
	}
	disk := index.ContentHash(raw)
	if ok && index.Fresh(indexed, raw) || !ok && slices.Contains(files, rel) || h.tried[rel] == disk {
		return nil
	}
	if h.tried == nil {
		h.tried = map[string]string{}
	}
	// A file the indexer leaves out (no parser, ignored) never reaches the store,
	// so remember the hash and do not open a session for it again.
	h.tried[rel] = disk
	sess, err := index.Open(ctx, h.cfg, h.logger, index.Options{Lexical: true})
	if err != nil {
		return err
	}
	defer sess.Close()
	res, err := sess.Sync(ctx, rel)
	if err == nil && res.Err != nil {
		err = res.Err
	}
	if err == nil && res.Lexical {
		h.embedInBackground()
	}
	return err
}

// embedLeftovers starts the background embedding when an earlier session or
// an index run left files without vectors.
func (h *tools) embedLeftovers() {
	hashes, err := h.store.FileHashes(h.bg)
	if err != nil {
		return
	}
	for _, stored := range hashes {
		if strings.HasSuffix(stored, index.LexicalSuffix) {
			h.embedInBackground()
			return
		}
	}
}

// embedInBackground adds the vectors that warm left out. Files are re-embedded
// only if their stored hash says they lack vectors, and an embedder that is not
// running ends the run at its first failure.
func (h *tools) embedInBackground() {
	if h.bg == nil || !h.embedding.CompareAndSwap(false, true) {
		return
	}
	h.bgWG.Add(1)
	go func() {
		defer h.bgWG.Done()
		defer h.embedding.Store(false)
		stats, err := index.Run(h.bg, h.cfg, h.logger, index.Options{})
		switch {
		case h.bg.Err() != nil:
		case err != nil:
			h.logger.Warn("background embedding", "err", err)
		case stats.Lexical > 0:
			h.logger.Warn("embedder unavailable, search stays lexical for now", "files", stats.Lexical)
		default:
			h.logger.Info("background embedding done", "files", stats.Files)
		}
	}()
}
