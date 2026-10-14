package index

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/ignore"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/split"
	"github.com/axitdev/onfly/internal/tokens"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/cespare/xxhash/v2"
)

const (
	maxFileBytes = 2 << 20
	embedBatch   = 32
)

type Options struct {
	CI       bool
	DryRun   bool
	Progress func(Event)
}

type Stats struct {
	Files   int
	Chunks  int
	Skipped int
	Failed  int
	Pruned  int
	Tokens  int
	Reused  int
}

type FileResult struct {
	Skipped bool
	Chunks  int
	Reused  int
	Tokens  int
	Omit    bool
	Err     error
}

// Failed reports a per-file failure that must not abort the run.
func (r FileResult) Failed() bool { return r.Err != nil }

type Session struct {
	cfg      config.Config
	logger   *slog.Logger
	opt      Options
	store    vectorstore.Store
	emb      embedder.Embedder
	router   *parser.Router
	filter   *ignore.Filter
	count    func(string) int
	progress func(Event)
	n        atomic.Int64
	total    int
	names    map[string]struct{}
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
	reportOpt(opt, "loading tokenizer")
	if _, err := tokens.Count("x"); err != nil {
		return nil, err
	}
	var store vectorstore.Store
	var emb embedder.Embedder
	if !opt.DryRun {
		reportOpt(opt, "opening database")
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
	reportOpt(opt, "loading parsers")
	router, err := parser.NewRouter(cfg.Indexer, cfg.ProjectRoot)
	if err != nil {
		if store != nil {
			store.Close()
		}
		return nil, err
	}
	filter, err := ignore.LoadFilter(cfg.ProjectRoot, cfg.Indexer.Exclude)
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
		filter: filter,
		count: func(s string) int {
			n, err := tokens.Count(s)
			if err != nil {
				return len(s) / 4
			}
			return n
		},
		progress: opt.Progress,
	}, nil
}

// ReloadIgnore re-reads every .gitignore under the project root.
func (s *Session) ReloadIgnore() error {
	filter, err := ignore.LoadFilter(s.cfg.ProjectRoot, s.cfg.Indexer.Exclude)
	if err != nil {
		return err
	}
	s.filter = filter
	return nil
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
	s.report("walking", "")
	files, err := walk(s.cfg.ProjectRoot, s.cfg.Indexer.Include, s.filter, s.router.HasParser)
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
	s.total = len(files)
	results, done, runErr := s.indexAll(ctx, files, hashes)
	for i, rel := range files {
		if !done[i] {
			continue
		}
		result := results[i]
		if result.Omit {
			continue
		}
		if result.Failed() {
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
		stats.Reused += result.Reused
		stats.Tokens += result.Tokens
	}
	if runErr != nil {
		return stats, runErr
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
	s.report("pruning", "")
	if err := s.prune(ctx, protected, &stats); err != nil {
		return stats, err
	}
	s.report("linking references", "")
	if err := s.linkRefs(ctx, nil); err != nil {
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

// indexAll runs indexFile for every path, with indexer.concurrency workers.
// done[i] is true when results[i] is valid. The first hard error stops the run.
func (s *Session) indexAll(ctx context.Context, files []string, hashes map[string]string) ([]FileResult, []bool, error) {
	results := make([]FileResult, len(files))
	done := make([]bool, len(files))
	workers := max(1, s.cfg.Indexer.Concurrency)
	if workers == 1 {
		for i, rel := range files {
			if err := ctx.Err(); err != nil {
				return results, done, err
			}
			s.n.Store(int64(i + 1))
			r, err := s.indexFile(ctx, rel, hashes[rel])
			if err != nil {
				return results, done, err
			}
			results[i], done[i] = r, true
		}
		return results, done, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg   sync.WaitGroup
		once sync.Once
		fail error
	)
	jobs := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				s.n.Store(int64(i + 1))
				r, err := s.indexFile(ctx, files[i], hashes[files[i]])
				if err != nil {
					once.Do(func() { fail = err; cancel() })
					continue
				}
				results[i], done[i] = r, true
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results, done, fail
}

func (s *Session) Sync(ctx context.Context, rel string) (FileResult, error) {
	rel = slash(rel)
	if !s.inScope(rel) {
		return FileResult{}, s.Drop(ctx, rel)
	}
	full := filepath.Join(s.cfg.ProjectRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err != nil {
		if os.IsNotExist(err) {
			return FileResult{}, s.Drop(ctx, rel)
		}
		s.logger.Warn("stat", "path", rel, "err", err)
		return FileResult{Err: fmt.Errorf("%s: %w", rel, err)}, nil
	}
	oldHash, _, err := s.store.FileHash(ctx, rel)
	if err != nil {
		return FileResult{}, err
	}
	result, err := s.indexFile(ctx, rel, oldHash)
	if err != nil || result.Failed() || result.Skipped {
		return result, err
	}
	if err := s.linkRefs(ctx, []string{rel}); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Session) deletePaths(ctx context.Context, paths []string) error {
	if s.store == nil || len(paths) == 0 {
		return nil
	}
	return s.store.DeleteFiles(ctx, paths)
}

func (s *Session) Drop(ctx context.Context, rel string) error {
	if err := s.deletePaths(ctx, []string{slash(rel)}); err != nil {
		return err
	}
	return s.linkRefs(ctx, []string{})
}

// knownPaths is every path the store holds chunks or a hash for.
func (s *Session) knownPaths(ctx context.Context) (map[string]struct{}, error) {
	hashes, err := s.store.FileHashes(ctx)
	if err != nil {
		return nil, err
	}
	listed, err := s.store.ListFiles(ctx)
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	for path := range hashes {
		set[slash(path)] = struct{}{}
	}
	for _, path := range listed {
		set[slash(path)] = struct{}{}
	}
	return set, nil
}

func (s *Session) DropAbsent(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	set, err := s.knownPaths(ctx)
	if err != nil {
		return err
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
		if !s.inScope(path) {
			drop = append(drop, path)
		}
	}
	if err := s.deletePaths(ctx, drop); err != nil {
		return err
	}
	return s.linkRefs(ctx, []string{})
}

func (s *Session) inScope(rel string) bool {
	rel = slash(rel)
	if s.filter.Skip(rel, false) {
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
	set, err := s.knownPaths(ctx)
	if err != nil {
		return err
	}
	prefix := rel + "/"
	var drop []string
	for path := range set {
		if path == rel || strings.HasPrefix(path, prefix) {
			drop = append(drop, path)
		}
	}
	if err := s.deletePaths(ctx, drop); err != nil {
		return err
	}
	return s.linkRefs(ctx, []string{})
}

// omit drops rel from the store (unless dry run) and tells the caller to ignore it.
func (s *Session) omit(ctx context.Context, rel string) (FileResult, error) {
	if !s.opt.DryRun {
		if err := s.deletePaths(ctx, []string{rel}); err != nil {
			return FileResult{}, err
		}
	}
	return FileResult{Omit: true}, nil
}

// readSource validates and reads a project file. A non-nil early result means
// the file must not be parsed: it was omitted (and dropped from the store), or
// it failed with a per-file error.
func (s *Session) readSource(ctx context.Context, rel string) ([]byte, *FileResult, error) {
	full := filepath.Join(s.cfg.ProjectRoot, filepath.FromSlash(rel))
	lst, lerr := os.Lstat(full)
	if lerr != nil {
		if os.IsNotExist(lerr) {
			return s.omitEarly(ctx, rel)
		}
		s.logger.Warn("stat", "path", rel, "err", lerr)
		s.report("failed", rel)
		return nil, &FileResult{Err: fmt.Errorf("%s: %w", rel, lerr)}, nil
	}
	if lst.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(full)
		if err != nil || !withinRoot(s.cfg.ProjectRoot, target) || !linkTargetOK(s.cfg.ProjectRoot, target, s.filter) {
			return s.omitEarly(ctx, rel)
		}
	}
	info, err := os.Stat(full)
	if err != nil {
		s.logger.Warn("stat", "path", rel, "err", err)
		s.report("failed", rel)
		return nil, &FileResult{Err: fmt.Errorf("%s: %w", rel, err)}, nil
	}
	if !info.Mode().IsRegular() {
		return s.omitEarly(ctx, rel)
	}
	if info.Size() > maxFileBytes {
		s.logger.Warn("skip large file", "path", rel, "size", info.Size())
		return s.omitEarly(ctx, rel)
	}
	body, err := os.ReadFile(full)
	if err != nil {
		s.logger.Warn("read", "path", rel, "err", err)
		s.report("failed", rel)
		return nil, &FileResult{Err: fmt.Errorf("%s: %w", rel, err)}, nil
	}
	return body, nil, nil
}

func (s *Session) omitEarly(ctx context.Context, rel string) ([]byte, *FileResult, error) {
	res, err := s.omit(ctx, rel)
	return nil, &res, err
}

func (s *Session) indexFile(ctx context.Context, rel, oldHash string) (FileResult, error) {
	rel = slash(rel)
	body, early, err := s.readSource(ctx, rel)
	if early != nil {
		return *early, err
	}
	full := filepath.Join(s.cfg.ProjectRoot, filepath.FromSlash(rel))
	hash := ContentHash(body)
	if oldHash != "" && oldHash == hash {
		s.report("unchanged", rel)
		return FileResult{Skipped: true}, nil
	}
	s.report("parsing", rel)
	chunks, err := s.router.Parse(ctx, full, body)
	if err != nil {
		s.logger.Warn("parse", "path", rel, "err", err)
		s.report("failed", rel)
		return FileResult{Err: fmt.Errorf("%s: %w", rel, err)}, nil
	}
	overlap := s.cfg.Embedder.MaxInputTokens * 12 / 100
	chunks = split.Split(chunks, s.cfg.Embedder.MaxInputTokens, overlap, s.count)
	for i := range chunks {
		chunks[i].FilePath = rel
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = map[string]string{}
		}
	}
	fillChunkLines(body, chunks)
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
	reused := 0
	if len(chunks) > 0 {
		s.report("embedding", rel)
		prev, perr := s.store.VectorsByFile(ctx, rel)
		if perr != nil {
			s.logger.Warn("vectors by file", "path", rel, "err", perr)
			prev = nil
		}
		embedded, reused, err = s.embedWithReuse(ctx, chunks, prev)
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
	return FileResult{Chunks: len(chunks), Reused: reused}, nil
}

// embedWithReuse fills in a stored vector for every chunk whose content the
// file already had, and embeds the rest. The result keeps chunk order.
func (s *Session) embedWithReuse(ctx context.Context, chunks []parser.Chunk, prev map[string][]float32) ([]vectorstore.EmbeddedChunk, int, error) {
	dims := s.cfg.Embedder.Dimensions
	out := make([]vectorstore.EmbeddedChunk, len(chunks))
	var todo []int
	reused := 0
	for i, ch := range chunks {
		if v, ok := prev[ch.Content]; ok && len(v) == dims {
			out[i] = vectorstore.EmbeddedChunk{Chunk: ch, Vector: v}
			reused++
			continue
		}
		todo = append(todo, i)
	}
	if len(todo) == 0 {
		return out, reused, nil
	}
	fresh := make([]parser.Chunk, len(todo))
	for j, i := range todo {
		fresh[j] = chunks[i]
	}
	got, err := embedChunks(ctx, s.emb, fresh, batchSize(s.opt))
	if err != nil {
		return nil, 0, err
	}
	for j, i := range todo {
		out[i] = got[j]
	}
	return out, reused, nil
}

// fillChunkLines sets start_line and end_line when the parser left both empty.
// The line is 1 plus the newlines before the first match of the chunk text.
// The end line adds the newlines inside that text. A missing match leaves both unset.
// Chunks arrive in file order, so each search starts after the previous match;
// repeated snippets then resolve to successive occurrences, not the first one.
func fillChunkLines(file []byte, chunks []parser.Chunk) {
	text := string(file)
	from := 0
	for i := range chunks {
		meta := chunks[i].Metadata
		if meta == nil {
			meta = map[string]string{}
			chunks[i].Metadata = meta
		}
		if strings.TrimSpace(meta["start_line"]) != "" || strings.TrimSpace(meta["end_line"]) != "" {
			continue
		}
		content := chunks[i].Content
		if content == "" {
			continue
		}
		idx := strings.Index(text[from:], content)
		if idx >= 0 {
			idx += from
		} else if idx = strings.Index(text, content); idx < 0 {
			continue
		}
		from = idx + 1
		start := 1 + strings.Count(text[:idx], "\n")
		meta["start_line"] = strconv.Itoa(start)
		meta["end_line"] = strconv.Itoa(start + strings.Count(content, "\n"))
	}
}

func (s *Session) prune(ctx context.Context, protected map[string]struct{}, stats *Stats) error {
	known, err := s.knownPaths(ctx)
	if err != nil {
		return err
	}
	var drop []string
	for path := range known {
		if _, ok := protected[path]; ok || !s.inScope(path) {
			continue
		}
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
