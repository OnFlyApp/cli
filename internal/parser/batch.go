package parser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
)

// A plugin that contains batchMarker understands "--batch": it prints
// {"ready":"onfly-batch/1"} and then answers each input line
// {"path":...,"content":<base64>} with {"chunks":[...]} or {"error":"..."} until
// its input ends. A plugin built for one file per run starts its runtime,
// which takes about half a second for a tree-sitter grammar, on every file; in
// batch mode that happens once per worker.
var batchMarker = []byte("onfly-batch/1")

const (
	batchStartTimeout = 30 * time.Second
	// A long-lived instance is replaced after this many files so that memory
	// the plugin's garbage collector has not returned does not add up.
	batchRecycleAfter = 2000
)

type batchProc struct {
	in     *io.PipeWriter
	inR    *io.PipeReader
	out    *bufio.Reader
	cancel context.CancelFunc
	done   chan struct{}
	errBuf *capBuf
	served int
}

func (b *batchProc) close() {
	b.cancel()
	_ = b.in.Close()
	_ = b.inR.Close()
	<-b.done
}

func (p *WasmParser) enableBatch(wasm []byte) {
	if !bytes.Contains(wasm, batchMarker) {
		return
	}
	p.batch.Store(true)
	n := runtime.NumCPU()
	p.slots = make(chan struct{}, n)
	p.idle = make(chan *batchProc, n)
}

func (p *WasmParser) startBatch() (*batchProc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	b := &batchProc{in: inW, inR: inR, out: bufio.NewReaderSize(outR, 1<<20), cancel: cancel, done: make(chan struct{}), errBuf: &capBuf{max: 1 << 20}}
	cfg := wazero.NewModuleConfig().WithName("").WithArgs("plugin", "--batch").WithStdin(inR).WithStdout(outW).WithStderr(b.errBuf)
	go func() {
		defer close(b.done)
		mod, err := p.rt.InstantiateModule(ctx, p.compiled, cfg)
		if mod != nil {
			_ = mod.Close(context.Background())
		}
		_ = outW.CloseWithError(errors.Join(io.EOF, err))
	}()
	timer := time.AfterFunc(batchStartTimeout, cancel)
	defer timer.Stop()
	line, err := b.out.ReadBytes('\n')
	var ready struct {
		Ready string `json:"ready"`
	}
	if err != nil || json.Unmarshal(line, &ready) != nil || ready.Ready != string(batchMarker) {
		b.close()
		return nil, fmt.Errorf("batch plugin did not start: %v %s", err, tail(b.errBuf.buf.Bytes()))
	}
	return b, nil
}

func (p *WasmParser) acquire(ctx context.Context) (*batchProc, error) {
	select {
	case b := <-p.idle:
		return b, nil
	default:
	}
	select {
	case p.slots <- struct{}{}:
		b, err := p.startBatch()
		if err != nil {
			<-p.slots
			return nil, err
		}
		return b, nil
	case b := <-p.idle:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *WasmParser) release(b *batchProc, healthy bool) {
	if healthy && b.served < batchRecycleAfter {
		p.idle <- b
		return
	}
	b.close()
	<-p.slots
}

type batchRequest struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

type batchResponse struct {
	Chunks []Chunk `json:"chunks"`
	Error  string  `json:"error"`
}

// errBatchDied means the long-lived process ended during this file (for
// example out of memory after many files); the file may still parse in a fresh
// one-shot run. errBatchStart means no batch process could be started.
var (
	errBatchDied  = errors.New("batch process died")
	errBatchStart = errors.New("batch process did not start")
)

func (p *WasmParser) parseBatch(ctx context.Context, filePath string, content []byte) (chunks []Chunk, fallback error, err error) {
	b, err := p.acquire(ctx)
	if err != nil {
		if ctx.Err() == nil {
			return nil, errBatchStart, err
		}
		return nil, nil, err
	}
	healthy := false
	defer func() { p.release(b, healthy) }()
	req, err := json.Marshal(batchRequest{Path: filePath, Content: content})
	if err != nil {
		return nil, nil, err
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(p.timeoutFor(len(content)), func() { timedOut.Store(true); b.cancel(); _ = b.in.Close() })
	defer timer.Stop()
	stop := context.AfterFunc(ctx, func() { b.cancel(); _ = b.in.Close() })
	defer stop()
	fail := func(what string, err error) ([]Chunk, error, error) {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if !timedOut.Load() {
			return nil, errBatchDied, fmt.Errorf("%s: %v", what, err)
		}
		msg := tail(b.errBuf.buf.Bytes())
		if h := b.errBuf.buf.Bytes(); len(h) > 600 {
			msg = string(h[:600]) + " ... " + msg
		}
		return nil, nil, fmt.Errorf("wasm parse: %s: %v %s", what, err, msg)
	}
	if _, err := b.in.Write(append(req, '\n')); err != nil {
		return fail("write", err)
	}
	line, err := b.out.ReadBytes('\n')
	if err != nil {
		return fail("read", err)
	}
	if len(line) > maxStdout {
		return nil, nil, fmt.Errorf("wasm parse: stdout exceeds %d bytes", maxStdout)
	}
	var resp batchResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return fail("decode", err)
	}
	healthy = true
	b.served++
	if resp.Error != "" {
		return nil, nil, fmt.Errorf("wasm parse: %s", resp.Error)
	}
	if resp.Chunks == nil {
		resp.Chunks = []Chunk{}
	}
	for i := range resp.Chunks {
		if resp.Chunks[i].FilePath == "" {
			resp.Chunks[i].FilePath = filePath
		}
		if resp.Chunks[i].Metadata == nil {
			resp.Chunks[i].Metadata = map[string]string{}
		}
	}
	return resp.Chunks, nil, nil
}

func (p *WasmParser) closeBatch() {
	if !p.batch.Load() {
		return
	}
	for {
		select {
		case b := <-p.idle:
			b.close()
			<-p.slots
		default:
			return
		}
	}
}
