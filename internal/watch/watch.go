package watch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/ignore"
	"github.com/axitdev/onfly/internal/index"
	"github.com/fsnotify/fsnotify"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	session, err := index.Open(ctx, cfg, logger, index.Options{})
	if err != nil {
		return err
	}
	filter, err := ignore.LoadFilter(cfg.ProjectRoot, cfg.Indexer.Exclude)
	if err != nil {
		session.Close()
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		session.Close()
		return err
	}
	if err := addDirs(w, cfg.ProjectRoot, filter); err != nil {
		w.Close()
		session.Close()
		return err
	}

	window := time.Duration(cfg.Indexer.DebounceMS) * time.Millisecond
	if window <= 0 {
		window = 2 * time.Second
	}
	var opMu sync.Mutex
	closed := false
	coal := newCoalescer(window, func(rel string) {
		opMu.Lock()
		defer opMu.Unlock()
		if closed || ctx.Err() != nil {
			return
		}
		full := filepath.Join(cfg.ProjectRoot, filepath.FromSlash(rel))
		if _, err := os.Stat(full); err != nil {
			if os.IsNotExist(err) {
				if err := session.DropTree(ctx, rel); err != nil {
					logger.Error("watch drop", "path", rel, "err", err)
				} else {
					logger.Info("removed " + rel)
				}
				return
			}
			logger.Error("watch stat", "path", rel, "err", err)
			return
		}
		res, err := session.Sync(ctx, rel)
		switch {
		case err != nil:
			logger.Error("watch sync", "path", rel, "err", err)
		case res.Failed():
			logger.Warn("could not index", "path", rel, "err", res.Err)
		case !res.Skipped:
			logger.Info("indexed "+rel, "chunks", res.Chunks, "reused", res.Reused)
		}
	})
	defer func() {
		coal.stop()
		opMu.Lock()
		closed = true
		session.Close()
		opMu.Unlock()
		w.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				opMu.Lock()
				rescan(w, cfg.ProjectRoot, filter, coal.push)
				if err := session.DropAbsent(ctx); err != nil {
					logger.Error("watch rescan", "err", err)
				}
				opMu.Unlock()
			}
			logger.Error("watch", "err", err)
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if !interesting(ev.Op) {
				continue
			}
			rel, skip := relEvent(cfg.ProjectRoot, ev.Name)
			if skip {
				continue
			}
			if filepath.Base(rel) == ".gitignore" && !ev.Has(fsnotify.Remove) {
				if next, err := ignore.LoadFilter(cfg.ProjectRoot, cfg.Indexer.Exclude); err == nil {
					filter = next
					opMu.Lock()
					if err := session.ReloadIgnore(); err != nil {
						logger.Error("watch ignore", "err", err)
					} else if err := session.DropAbsent(ctx); err != nil {
						logger.Error("watch ignore", "err", err)
					}
					opMu.Unlock()
					rescan(w, cfg.ProjectRoot, filter, coal.push)
				}
				continue
			}
			if ev.Has(fsnotify.Remove) {
				coal.push(rel)
				continue
			}
			if ev.Has(fsnotify.Create) {
				info, err := os.Stat(ev.Name)
				if err == nil && info.IsDir() {
					if !skipDir(rel, filter) {
						watchTree(w, cfg.ProjectRoot, ev.Name, filter, coal.push)
					}
					continue
				}
			}
			coal.push(rel)
		}
	}
}

func interesting(op fsnotify.Op) bool {
	return op.Has(fsnotify.Write) || op.Has(fsnotify.Create) || op.Has(fsnotify.Rename) || op.Has(fsnotify.Remove)
}

func watchTree(w *fsnotify.Watcher, root, dir string, filter *ignore.Filter, push func(string)) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, skip := relEvent(root, path)
		if skip {
			if d.IsDir() && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if rel != "." && skipDir(rel, filter) {
				return filepath.SkipDir
			}
			if err := w.Add(path); err != nil {
				return nil
			}
			return nil
		}
		push(rel)
		return nil
	})
}

func rescan(w *fsnotify.Watcher, root string, filter *ignore.Filter, push func(string)) {
	watchTree(w, root, root, filter, push)
}

func addDirs(w *fsnotify.Watcher, root string, filter *ignore.Filter) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "." && skipDir(rel, filter) {
			return filepath.SkipDir
		}
		return w.Add(path)
	})
}

func skipDir(rel string, filter *ignore.Filter) bool {
	return filter.Skip(rel, true)
}

func relEvent(root, name string) (string, bool) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", true
	}
	absName, err := filepath.Abs(name)
	if err != nil {
		return "", true
	}
	rel, err := filepath.Rel(absRoot, absName)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", true
	}
	rel = filepath.ToSlash(rel)
	for _, part := range strings.Split(rel, "/") {
		if part == ".git" || part == ".onfly" {
			return "", true
		}
	}
	return rel, false
}

type coalescer struct {
	mu      sync.Mutex
	window  time.Duration
	timers  map[string]*time.Timer
	gen     map[string]int
	stopped bool
	fire    func(string)
}

func newCoalescer(window time.Duration, fire func(string)) *coalescer {
	return &coalescer{
		window: window,
		timers: map[string]*time.Timer{},
		gen:    map[string]int{},
		fire:   fire,
	}
}

func (c *coalescer) push(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	gen := c.gen[path] + 1
	c.gen[path] = gen
	if t, ok := c.timers[path]; ok {
		t.Stop()
	}
	c.timers[path] = time.AfterFunc(c.window, func() {
		c.mu.Lock()
		if c.stopped || c.gen[path] != gen {
			c.mu.Unlock()
			return
		}
		delete(c.timers, path)
		delete(c.gen, path)
		c.mu.Unlock()
		c.fire(path)
	})
}

func (c *coalescer) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	for _, t := range c.timers {
		t.Stop()
	}
}
