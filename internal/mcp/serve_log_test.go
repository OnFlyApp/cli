package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/logger"
)

// stdout is the protocol: whatever the logger is set to, only JSON-RPC may be on it.
func TestServeLogsNeverReachTheProtocolStream(t *testing.T) {
	h, dir := warmProject(t)
	cfg := h.cfg
	cfg.Server.LogFile = filepath.Join(dir, ".onfly", "server.log")
	cfg.Server.LogLevel = "info"
	for _, opts := range []logger.Options{{}, {Verbose: true, JSON: true}} {
		inR, inW := io.Pipe()
		var out lockedBuffer
		outR, outW := io.Pipe()
		go func() { io.Copy(&out, outR) }()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- ServeWith(ctx, cfg, inR, outW, opts) }()
		msgs := []string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		}
		for _, m := range msgs {
			io.WriteString(inW, m+"\n")
		}
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(out.String(), `"id":2`) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		inW.Close()
		cancel()
		<-done
		outW.Close()

		sc := bufio.NewScanner(strings.NewReader(out.String()))
		lines := 0
		for sc.Scan() {
			var msg map[string]any
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil || msg["jsonrpc"] != "2.0" {
				t.Fatalf("stdout carries something other than JSON-RPC: %q", sc.Text())
			}
			lines++
		}
		if lines < 2 {
			t.Fatalf("expected the initialize and tools/list replies, got %d lines: %q", lines, out.String())
		}
	}
	log, err := os.ReadFile(cfg.Server.LogFile)
	if err != nil || !strings.Contains(string(log), "server run start") {
		t.Fatalf("the server log is where the records go: %q %v", log, err)
	}
	if !strings.Contains(string(log), `"msg":"server run start"`) {
		t.Fatalf("--json-logs writes JSON records: %q", log)
	}
}

// lockedBuffer is a buffer the test can read while the server is writing it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
