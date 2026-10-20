package index

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	CI     bool
	DryRun bool
	// Lexical stores chunks without calling the embedder, keeping the vectors a
	// file already has. Search by name, text and path works at once; a later run
	// without Lexical adds the missing vectors.
	Lexical  bool
	Progress func(Event)
}

// LexicalSuffix marks the stored hash of a file whose chunks lack vectors, so
// a run that can embed does not take the file for unchanged.
const LexicalSuffix = "+lex"

// Fresh reports whether the hash stored for a file describes this content.
func Fresh(stored string, content []byte) bool {
	h := ContentHash(content)
	return stored == h || stored == h+LexicalSuffix
}

type Stats struct {
	Files   int
	Chunks  int
	Skipped int
	Failed  int
	Pruned  int
	Tokens  int
	Reused  int
	// Lexical counts files stored without vectors because the embedder was
	// skipped or unreachable.
	Lexical int
}

type FileResult struct {
	Skipped bool
	Chunks  int
	Reused  int
	Tokens  int
	Omit    bool
	Lexical bool
	Err     error
}

func (r FileResult) Failed() bool { return r.Err != nil }

type Session struct {
	cfg      config.Config
	logger   *slog.Logger
	opt      Options
	store    vectorstore.Store
	emb      embedder.Embedder
	chat     embedder.Chat
	sums     *summaryCache
	alt      embedder.Embedder
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
	var chat embedder.Chat
	var alt embedder.Embedder
	if !opt.DryRun {
		var err error
		if chat, err = embedder.NewChat(cfg.Indexer.Summaries.LLM); err != nil {
			return nil, fmt.Errorf("indexer.summaries: %w", err)
		}
		reportOpt(opt, "opening database")
		store, err = vectorstore.New(ctx, cfg.Database)
		if err != nil {
			return nil, err
		}
		if err := vectorstore.InitAll(ctx, store, cfg); err != nil {
			store.Close()
			return nil, err
		}
		emb, err = embedder.New(cfg.Embedder)
		if err != nil {
			store.Close()
			return nil, err
		}
		emb = embedder.WithBreaker(emb, time.Minute)
		if cfg.Embedder.Alt != nil {
			if alt, err = embedder.New(*cfg.Embedder.Alt); err != nil {
				store.Close()
				return nil, fmt.Errorf("embedder.alt: %w", err)
			}
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
	var sums *summaryCache
	if chat != nil {
		path := cfg.Indexer.Summaries.Cache
		if path == "" {
			path = filepath.Join(".onfly", "summaries.jsonl")
		}
		if path != "none" && !filepath.IsAbs(path) {
			path = filepath.Join(cfg.ProjectRoot, path)
		}
		sums = openSummaryCache(path)
	}
	return &Session{
		cfg:    cfg,
		logger: logger,
		opt:    opt,
		store:  store,
		emb:    emb,
		chat:   chat,
		alt:    alt,
		sums:   sums,
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
		if result.Lexical {
			stats.Lexical++
		}
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

func (s *Session) indexAll(ctx context.Context, files []string, hashes map[string]string) ([]FileResult, []bool, error) {
	results := make([]FileResult, len(files))
	done := make([]bool, len(files))
	workers := max(1, s.cfg.Indexer.Concurrency)
	if s.opt.Lexical && s.cfg.Indexer.Concurrency <= 0 {
		// Nothing waits on the network, so parsing is the cost: use the cores.
		workers = runtime.NumCPU()
	}
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

func (s *Session) omit(ctx context.Context, rel string) (FileResult, error) {
	if !s.opt.DryRun {
		if err := s.deletePaths(ctx, []string{rel}); err != nil {
			return FileResult{}, err
		}
	}
	return FileResult{Omit: true}, nil
}

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
	if !s.cfg.Indexer.IndexGenerated && isGenerated(rel, body) {
		return s.omitEarly(ctx, rel)
	}
	return body, nil, nil
}

// isGenerated recognises machine-written files: they repeat the shape of the
// code they come from, are not written by anyone, and in a large repository
// they are a fifth of the chunks (Kubernetes: zz_generated, protobuf stubs).
func isGenerated(rel string, body []byte) bool {
	base := strings.ToLower(path.Base(rel))
	if strings.HasPrefix(base, "zz_generated") || strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, ".pb.gw.go") {
		return true
	}
	head := strings.ToLower(string(body[:min(len(body), 1024)]))
	return strings.Contains(head, "code generated") && strings.Contains(head, "do not edit") || strings.Contains(head, "@generated")
}

func (s *Session) omitEarly(ctx context.Context, rel string) ([]byte, *FileResult, error) {
	res, err := s.omit(ctx, rel)
	return nil, &res, err
}

// importTypes are one-line declarations of what a file imports or which
// namespace it is in. Their name is a full class path, so they match queries
// better than the classes they name; resolve reads imports from the source.
var importTypes = []string{"use", "namespace", "import"}

func dropTypes(chunks []parser.Chunk, skip []string) []parser.Chunk {
	if len(skip) == 0 {
		return chunks
	}
	drop := make(map[string]bool, len(skip))
	for _, t := range skip {
		drop[strings.ToLower(strings.TrimSpace(t))] = true
	}
	out := chunks[:0:0]
	for _, c := range chunks {
		if !drop[strings.ToLower(c.Type)] {
			out = append(out, c)
		}
	}
	return out
}

func (s *Session) indexFile(ctx context.Context, rel, oldHash string) (FileResult, error) {
	rel = slash(rel)
	body, early, err := s.readSource(ctx, rel)
	if early != nil {
		return *early, err
	}
	full := filepath.Join(s.cfg.ProjectRoot, filepath.FromSlash(rel))
	hash := ContentHash(body)
	if oldHash != "" && (oldHash == hash || s.opt.Lexical && oldHash == hash+LexicalSuffix) {
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
	chunks = dropTypes(chunks, append(slices.Clone(s.cfg.Indexer.SkipTypes), importTypes...))
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
		prev, perr := s.store.VectorsByFile(ctx, rel)
		if perr != nil {
			s.logger.Warn("vectors by file", "path", rel, "err", perr)
			prev = nil
		}
		if !s.opt.Lexical {
			s.report("embedding", rel)
			embedded, reused, err = s.embedFile(ctx, rel, chunks, prev)
			if err != nil {
				if ctx.Err() != nil {
					return FileResult{}, fmt.Errorf("embed %s: %w", rel, err)
				}
				s.logger.Warn("embedder unavailable, storing the file without vectors", "path", rel, "err", err)
				embedded = nil
			}
		}
		if embedded == nil {
			embedded, reused = withoutVectors(chunks, prev, s.cfg.Embedder.Dimensions)
		}
		for i := range embedded {
			embedded[i].Workspace = s.cfg.Workspace
		}
	}
	lexical := slices.ContainsFunc(embedded, func(e vectorstore.EmbeddedChunk) bool { return len(e.Vector) == 0 })
	stored := hash
	if lexical {
		stored += LexicalSuffix
	}
	if err := s.store.CommitFile(ctx, rel, stored, embedded); err != nil {
		return FileResult{}, fmt.Errorf("save %s: %w", rel, err)
	}
	return FileResult{Chunks: len(chunks), Reused: reused, Lexical: lexical}, nil
}

func (s *Session) embedFile(ctx context.Context, rel string, chunks []parser.Chunk, prev map[string][]float32) ([]vectorstore.EmbeddedChunk, int, error) {
	if s.chat != nil {
		s.report("summarising", rel)
		if prev == nil {
			prev = map[string][]float32{}
		}
		prev = s.addSummaries(ctx, rel, chunks, prev)
	}
	embedded, reused, err := s.embedWithReuse(ctx, chunks, prev)
	if err != nil {
		return nil, 0, err
	}
	if err := s.addExtraVectors(ctx, rel, embedded, prev); err != nil {
		return nil, 0, err
	}
	return embedded, reused, nil
}

// withoutVectors keeps the vector of every chunk whose text is unchanged and
// leaves the rest empty. It also counts the vectors kept.
func withoutVectors(chunks []parser.Chunk, prev map[string][]float32, dims int) ([]vectorstore.EmbeddedChunk, int) {
	out := make([]vectorstore.EmbeddedChunk, len(chunks))
	kept := 0
	for i, c := range chunks {
		out[i] = vectorstore.EmbeddedChunk{Chunk: c}
		if v, ok := prev[c.Content]; ok && len(v) == dims {
			out[i].Vector = v
			kept++
		}
	}
	return out, kept
}

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
	got, err := embedChunks(ctx, s.emb, fresh, batchSize(s.opt), s.cfg.Indexer.WithContext())
	if err != nil {
		return nil, 0, err
	}
	for j, i := range todo {
		out[i] = got[j]
	}
	return out, reused, nil
}

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
		if _, ok := protected[path]; ok {
			continue
		}
		if s.cfg.Workspace != "" && !s.inScope(path) {
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

func embedChunks(ctx context.Context, emb embedder.Embedder, chunks []parser.Chunk, batch int, withContext bool) ([]vectorstore.EmbeddedChunk, error) {
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
			texts[i] = embedText(chunk, withContext)
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

func embedText(c parser.Chunk, withContext bool) string {
	summary := c.Metadata["summary"]
	if !withContext && summary == "" {
		return c.Content
	}
	var b strings.Builder
	if withContext {
		b.WriteString("path: " + c.FilePath + "\n")
		if c.Type != "" {
			b.WriteString("kind: " + c.Type + "\n")
		}
		if name := c.Metadata["name"]; name != "" {
			sym := name
			if r := c.Metadata["receiver"]; r != "" {
				sym = r + "." + name
			}
			b.WriteString("symbol: " + sym + "\n")
		}
	}
	if summary != "" {
		b.WriteString("summary: " + summary + "\n")
	}
	b.WriteString("\n")
	b.WriteString(c.Content)
	return b.String()
}
