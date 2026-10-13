package index

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/ignore"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/split"
	"github.com/axitdev/onfly/internal/tokens"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/cespare/xxhash/v2"
)

const (
	maxFileBytes = 2 << 20
	embedBatch   = 32
)

type Options struct {
	CI     bool
	DryRun bool
}

type Stats struct {
	Files   int
	Chunks  int
	Skipped int
	Failed  int
	Pruned  int
	Tokens  int
}

type FileResult struct {
	Skipped bool
	Chunks  int
	Tokens  int
	Failed  bool
	Omit    bool
	Err     error
}

type Session struct {
	cfg    config.Config
	logger *slog.Logger
	opt    Options
	store  vectorstore.Store
	emb    embedder.Embedder
	router *parser.Router
	ign    *ignore.Set
	count  func(string) int
}

func ContentHash(b []byte) string {
	return fmt.Sprintf("%016x", xxhash.Sum64(b))
}

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger, opt Options) (Stats, error) {
	session, err := Open(ctx, cfg, logger, opt)
	if err != nil {
		return Stats{}, err
	}
	defer session.Close()
	return session.Run(ctx)
}

func Open(ctx context.Context, cfg config.Config, logger *slog.Logger, opt Options) (*Session, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	config.ApplyDefaults(&cfg)
	if _, err := tokens.Count("x"); err != nil {
		return nil, err
	}
	var store vectorstore.Store
	var emb embedder.Embedder
	if !opt.DryRun {
		var err error
		store, err = vectorstore.New(ctx, cfg.Database)
		if err != nil {
			return nil, err
		}
		if err := store.Init(ctx, cfg.Embedder.Dimensions); err != nil {
			store.Close()
			return nil, err
		}
		emb, err = embedder.New(cfg.Embedder)
		if err != nil {
			store.Close()
			return nil, err
		}
	}
	router, err := parser.NewRouter(cfg.Indexer, cfg.ProjectRoot)
	if err != nil {
		if store != nil {
			store.Close()
		}
		return nil, err
	}
	ign, err := ignore.Load(cfg.ProjectRoot)
	if err != nil {
		router.Close()
		if store != nil {
			store.Close()
		}
		return nil, err
	}
	return &Session{
		cfg:    cfg,
		logger: logger,
		opt:    opt,
		store:  store,
		emb:    emb,
		router: router,
		ign:    ign,
		count: func(s string) int {
			n, err := tokens.Count(s)
			if err != nil {
				return len(s) / 4
			}
			return n
		},
	}, nil
}

func (s *Session) Close() error {
	var err error
	if s.router != nil {
		err = s.router.Close()
	}
	if s.store != nil {
		if cerr := s.store.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

func (s *Session) Run(ctx context.Context) (Stats, error) {
	files, err := walk(s.cfg.ProjectRoot, s.cfg.Indexer.Include, s.cfg.Indexer.Exclude, s.ign, s.router.HasParser)
	if err != nil {
		return Stats{}, err
	}
	hashes := map[string]string{}
	if !s.opt.DryRun {
		hashes, err = s.store.FileHashes(ctx)
		if err != nil {
			return Stats{}, err
		}
	}
	var stats Stats
	var first error
	protected := map[string]struct{}{}
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		result, err := s.indexFile(ctx, rel, hashes[rel])
		if err != nil {
			return stats, err
		}
		if result.Omit {
			continue
		}
		if result.Failed {
			stats.Failed++
			protected[rel] = struct{}{}
			if first == nil {
				first = result.Err
			}
			continue
		}
		protected[rel] = struct{}{}
		if result.Skipped {
			stats.Skipped++
			continue
		}
		stats.Files++
		stats.Chunks += result.Chunks
		stats.Tokens += result.Tokens
	}
	if s.opt.DryRun {
		if stats.Failed > 0 {
			if first == nil {
				first = fmt.Errorf("index failed")
			}
			return stats, fmt.Errorf("index: %d files failed: %w", stats.Failed, first)
		}
		return stats, nil
	}
	if err := s.prune(ctx, protected, &stats); err != nil {
		return stats, err
	}
	if stats.Failed > 0 {
		if first == nil {
			first = fmt.Errorf("index failed")
		}
		return stats, fmt.Errorf("index: %d files failed: %w", stats.Failed, first)
	}
	return stats, nil
}

func (s *Session) Sync(ctx context.Context, rel string) (FileResult, error) {
	rel = slash(rel)
	if !s.eligible(rel) {
		return FileResult{}, s.Drop(ctx, rel)
	}
	full := filepath.Join(s.cfg.ProjectRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err != nil {
		if os.IsNotExist(err) {
			return FileResult{}, s.Drop(ctx, rel)
		}
		return FileResult{Failed: true}, nil
	}
	hashes, err := s.store.FileHashes(ctx)
	if err != nil {
		return FileResult{}, err
	}
	return s.indexFile(ctx, rel, hashes[rel])
}

func (s *Session) Drop(ctx context.Context, rel string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteFiles(ctx, []string{slash(rel)})
}

func (s *Session) DropAbsent(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	hashes, err := s.store.FileHashes(ctx)
	if err != nil {
		return err
	}
	listed, err := s.store.ListFiles(ctx)
	if err != nil {
		return err
	}
	set := map[string]struct{}{}
	for path := range hashes {
		set[slash(path)] = struct{}{}
	}
	for _, path := range listed {
		set[slash(path)] = struct{}{}
	}
	var drop []string
	for path := range set {
		full := filepath.Join(s.cfg.ProjectRoot, filepath.FromSlash(path))
		if _, err := os.Lstat(full); err != nil {
			if os.IsNotExist(err) {
				drop = append(drop, path)
			}
			continue
		}
		if !s.eligible(path) {
			drop = append(drop, path)
		}
	}
	return s.store.DeleteFiles(ctx, drop)
}

func (s *Session) eligible(rel string) bool {
	return s.inScope(rel)
}

func (s *Session) inScope(rel string) bool {
	rel = slash(rel)
	if s.ign != nil && s.ign.Ignored(rel, false) {
		return false
	}
	skip := map[string]bool{".onfly": true, ".git": true}
	for _, name := range s.cfg.Indexer.Exclude {
		if name != "" {
			skip[name] = true
		}
	}
	if hitSegment(rel, skip) {
		return false
	}
	ok, err := matchAny(rel, s.cfg.Indexer.Include)
	if err != nil || !ok || !s.router.HasParser(rel) {
		return false
	}
	return true
}

func (s *Session) DropTree(ctx context.Context, rel string) error {
	if s.store == nil {
		return nil
	}
	rel = slash(rel)
	hashes, err := s.store.FileHashes(ctx)
	if err != nil {
		return err
	}
	listed, err := s.store.ListFiles(ctx)
	if err != nil {
		return err
	}
	set := map[string]struct{}{}
	for path := range hashes {
		set[slash(path)] = struct{}{}
	}
	for _, path := range listed {
		set[slash(path)] = struct{}{}
	}
	prefix := rel + "/"
	var drop []string
	for path := range set {
		if path == rel || strings.HasPrefix(path, prefix) {
			drop = append(drop, path)
		}
	}
	return s.store.DeleteFiles(ctx, drop)
}

func (s *Session) indexFile(ctx context.Context, rel, oldHash string) (FileResult, error) {
	rel = slash(rel)
	full := filepath.Join(s.cfg.ProjectRoot, filepath.FromSlash(rel))
	lst, lerr := os.Lstat(full)
	if lerr != nil {
		if os.IsNotExist(lerr) {
			if !s.opt.DryRun {
				if err := s.Drop(ctx, rel); err != nil {
					return FileResult{}, err
				}
			}
			return FileResult{Omit: true}, nil
		}
		s.logger.Warn("stat", "path", rel, "err", lerr)
		return FileResult{Failed: true, Err: fmt.Errorf("%s: %w", rel, lerr)}, nil
	}
	if lst.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(full)
		if err != nil || !withinRoot(s.cfg.ProjectRoot, target) || !linkTargetOK(s.cfg.ProjectRoot, target, s.ign, s.cfg.Indexer.Exclude) {
			if !s.opt.DryRun {
				if err := s.Drop(ctx, rel); err != nil {
					return FileResult{}, err
				}
			}
			return FileResult{Omit: true}, nil
		}
	}
	info, err := os.Stat(full)
	if err != nil {
		s.logger.Warn("stat", "path", rel, "err", err)
		return FileResult{Failed: true, Err: fmt.Errorf("%s: %w", rel, err)}, nil
	}
	if !info.Mode().IsRegular() {
		if !s.opt.DryRun {
			if err := s.Drop(ctx, rel); err != nil {
				return FileResult{}, err
			}
		}
		return FileResult{Omit: true}, nil
	}
	if info.Size() > maxFileBytes {
		s.logger.Warn("skip large file", "path", rel, "size", info.Size())
		if !s.opt.DryRun {
			if err := s.Drop(ctx, rel); err != nil {
				return FileResult{}, err
			}
		}
		return FileResult{Omit: true}, nil
	}
	body, err := os.ReadFile(full)
	if err != nil {
		s.logger.Warn("read", "path", rel, "err", err)
		return FileResult{Failed: true, Err: fmt.Errorf("%s: %w", rel, err)}, nil
	}
	hash := ContentHash(body)
	if oldHash != "" && oldHash == hash {
		return FileResult{Skipped: true}, nil
	}
	chunks, err := s.router.Parse(full, body)
	if err != nil {
		s.logger.Warn("parse", "path", rel, "err", err)
		return FileResult{Failed: true, Err: fmt.Errorf("%s: %w", rel, err)}, nil
	}
	overlap := s.cfg.Embedder.MaxInputTokens * 12 / 100
	chunks = split.Split(chunks, s.cfg.Embedder.MaxInputTokens, overlap, s.count)
	for i := range chunks {
		chunks[i].FilePath = rel
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = map[string]string{}
		}
	}
	if s.opt.DryRun {
		n := 0
		for _, chunk := range chunks {
			count, err := tokens.Count(chunk.Content)
			if err != nil {
				return FileResult{}, err
			}
			n += count
		}
		return FileResult{Chunks: len(chunks), Tokens: n}, nil
	}
	var embedded []vectorstore.EmbeddedChunk
	if len(chunks) > 0 {
		embedded, err = embedChunks(ctx, s.emb, chunks, batchSize(s.opt))
		if err != nil {
			return FileResult{}, fmt.Errorf("embed %s: %w", rel, err)
		}
		for i := range embedded {
			embedded[i].Workspace = s.cfg.Workspace
		}
	}
	if err := s.store.CommitFile(ctx, rel, hash, embedded); err != nil {
		return FileResult{}, fmt.Errorf("save %s: %w", rel, err)
	}
	return FileResult{Chunks: len(chunks)}, nil
}

func (s *Session) prune(ctx context.Context, protected map[string]struct{}, stats *Stats) error {
	hashes, err := s.store.FileHashes(ctx)
	if err != nil {
		return err
	}
	listed, err := s.store.ListFiles(ctx)
	if err != nil {
		return err
	}
	dropSet := map[string]struct{}{}
	consider := func(path string) {
		path = slash(path)
		if _, ok := protected[path]; ok || !s.inScope(path) {
			return
		}
		dropSet[path] = struct{}{}
	}
	for path := range hashes {
		consider(path)
	}
	for _, path := range listed {
		consider(path)
	}
	var drop []string
	for path := range dropSet {
		drop = append(drop, path)
	}
	sort.Strings(drop)
	if err := s.store.DeleteFiles(ctx, drop); err != nil {
		return err
	}
	stats.Pruned = len(drop)
	return nil
}

func batchSize(opt Options) int {
	if opt.CI {
		return 128
	}
	return embedBatch
}

func embedChunks(ctx context.Context, emb embedder.Embedder, chunks []parser.Chunk, batch int) ([]vectorstore.EmbeddedChunk, error) {
	if batch < 1 {
		batch = embedBatch
	}
	out := make([]vectorstore.EmbeddedChunk, 0, len(chunks))
	for start := 0; start < len(chunks); start += batch {
		end := start + batch
		if end > len(chunks) {
			end = len(chunks)
		}
		part := chunks[start:end]
		texts := make([]string, len(part))
		for i, chunk := range part {
			texts[i] = chunk.Content
		}
		vecs, err := emb.EmbedBatch(ctx, texts)
		if err != nil {
			return nil, err
		}
		for i, chunk := range part {
			out = append(out, vectorstore.EmbeddedChunk{Chunk: chunk, Vector: vecs[i]})
		}
	}
	return out, nil
}

func walk(root string, include, exclude []string, ign *ignore.Set, want func(string) bool) ([]string, error) {
	skip := map[string]bool{".onfly": true, ".git": true}
	for _, name := range exclude {
		if name != "" {
			skip[name] = true
		}
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, evalErr := filepath.EvalSymlinks(path)
			info, statErr := os.Stat(path)
			if evalErr != nil || statErr != nil || info.IsDir() || !withinRoot(root, target) || !linkTargetOK(root, target, ign, exclude) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = slash(rel)
		if rel == "." {
			return nil
		}
		if ign != nil && ign.Ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if hitSegment(rel, skip) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ok, err := matchAny(rel, include)
		if err != nil {
			return err
		}
		if !ok || !want(rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func slash(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), `\`, "/")
}

func resolvedRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return filepath.Clean(root)
}

func withinRoot(root, path string) bool {
	root = resolvedRoot(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func linkTargetOK(root, target string, ign *ignore.Set, exclude []string) bool {
	root = resolvedRoot(root)
	rel, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return false
	}
	rel = slash(rel)
	if ignore.Sensitive(rel) || (ign != nil && ign.Ignored(rel, false)) {
		return false
	}
	skip := map[string]bool{".onfly": true, ".git": true}
	for _, name := range exclude {
		if name != "" {
			skip[name] = true
		}
	}
	return !hitSegment(rel, skip)
}

func hitSegment(rel string, skip map[string]bool) bool {
	for _, part := range strings.Split(rel, "/") {
		if skip[part] {
			return true
		}
	}
	return false
}

func matchAny(rel string, patterns []string) (bool, error) {
	for _, pattern := range patterns {
		ok, err := doublestar.PathMatch(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("indexer include %q: %w", pattern, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
