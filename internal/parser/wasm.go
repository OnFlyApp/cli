package parser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const (
	wasmMemoryPages = 4096
	parseTimeout    = 5 * time.Second
)

// WasmParser runs a WASI command module. The plugin contract, matching
// github.com/OnFlyApp/ext-parser-go, is: argv[1] is the file path, stdin is
// the file bytes, stdout is a JSON array of Chunk.
type WasmParser struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	mu       sync.Mutex
	closed   bool
}

func newWasmParser(ctx context.Context, path string) (*WasmParser, error) {
	wasm, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(wasmMemoryPages).
		WithCloseOnContextDone(true))
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wasm compile: %w", err)
	}
	return &WasmParser{rt: rt, compiled: compiled}, nil
}

func (p *WasmParser) Parse(ctx context.Context, filePath string, content []byte) ([]Chunk, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("wasm parser closed")
	}
	stdout := &maxBuf{max: maxStdout}
	stderr := &capBuf{max: 1 << 20}
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs("plugin", filePath).
		WithStdin(bytes.NewReader(content)).
		WithStdout(stdout).
		WithStderr(stderr)
	ctx, cancel := context.WithTimeout(ctx, parseTimeout)
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
	return p.rt.Close(context.Background())
}
