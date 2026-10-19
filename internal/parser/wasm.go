package parser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const (
	wasmMemoryPages = 8192
	parseTimeout    = 5 * time.Second
)

type WasmParser struct {
	rt       wazero.Runtime
	cache    wazero.CompilationCache
	compiled wazero.CompiledModule
	mu       sync.RWMutex // parses share the lock; Close takes it alone
	closed   bool
	Timeout  time.Duration
	batch    atomic.Bool // the plugin answers many files per run
	slots    chan struct{}
	idle     chan *batchProc
}

func timeoutOr(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return parseTimeout
}

// perKiB is what a parse may take per KiB of source on top of the base timeout
// when none is configured: a 17 MB PHP parser needs a few seconds for a 70 KB
// file, more when every core is busy.
const perKiB = 300 * time.Millisecond

// timeoutFor is the configured timeout, or the default grown with the file.
func (p *WasmParser) timeoutFor(size int) time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return parseTimeout + time.Duration(size>>10)*perKiB
}

func newWasmParser(ctx context.Context, path string) (*WasmParser, error) {
	wasm, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rc := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(wasmMemoryPages).
		WithCloseOnContextDone(true)
	// Compiling a language parser takes about a second; the cache on disk makes
	// every later process start skip it. Without a cache directory it compiles
	// each time.
	var cache wazero.CompilationCache
	if dir, err := os.UserCacheDir(); err == nil {
		if cache, err = wazero.NewCompilationCacheWithDir(filepath.Join(dir, "onfly", "wasm")); err == nil {
			rc = rc.WithCompilationCache(cache)
		} else {
			cache = nil
		}
	}
	rt := wazero.NewRuntimeWithConfig(ctx, rc)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		_ = rt.Close(ctx)
		if cache != nil {
			_ = cache.Close(ctx)
		}
		return nil, fmt.Errorf("wasm compile: %w", err)
	}
	p := &WasmParser{rt: rt, compiled: compiled, cache: cache}
	p.enableBatch(wasm)
	return p, nil
}

func (p *WasmParser) Parse(ctx context.Context, filePath string, content []byte) ([]Chunk, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return nil, fmt.Errorf("wasm parser closed")
	}
	if p.batch.Load() {
		chunks, fallback, err := p.parseBatch(ctx, filePath, content)
		switch fallback {
		case nil:
			return chunks, err
		case errBatchStart:
			// This plugin keeps running once per file.
			p.batch.Store(false)
		}
		// errBatchDied: this file runs once on its own below.
	}
	stdout := &maxBuf{max: maxStdout}
	stderr := &capBuf{max: 1 << 20}
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs("plugin", filePath).
		WithStdin(bytes.NewReader(content)).
		WithStdout(stdout).
		WithStderr(stderr)
	ctx, cancel := context.WithTimeout(ctx, p.timeoutFor(len(content)))
	defer cancel()
	mod, err := p.rt.InstantiateModule(ctx, p.compiled, cfg)
	if mod != nil {
		_ = mod.Close(context.Background())
	}
	if err != nil {
		var exit *sys.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 0 {
			err = nil
		}
	}
	if stdout.err != nil {
		return nil, stdout.err
	}
	if err != nil {
		msg := tail(stderr.buf.Bytes())
		if msg == "" {
			return nil, fmt.Errorf("wasm parse: %w", err)
		}
		return nil, fmt.Errorf("wasm parse: %w: %s", err, msg)
	}
	return decodeChunks(filePath, stdout.buf.Bytes())
}

func (p *WasmParser) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.closeBatch()
	err := p.rt.Close(context.Background())
	if p.cache != nil {
		if cerr := p.cache.Close(context.Background()); err == nil {
			err = cerr
		}
	}
	return err
}
