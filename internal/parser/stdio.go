package parser

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

type StdioParser struct {
	command string
	mu      sync.Mutex
}

func newStdioParser(command string) *StdioParser {
	return &StdioParser{command: command}
}

func shellCommand(ctx context.Context, command, filePath string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/c", command+" "+quoteCmd(filePath))
	}
	return exec.CommandContext(ctx, "sh", "-c", command+` "$@"`, "sh", filePath)
}

func quoteCmd(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func (p *StdioParser) Parse(ctx context.Context, filePath string, content []byte) ([]Chunk, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, parseTimeout)
	defer cancel()
	cmd := shellCommand(ctx, p.command, filePath)
	prepareCmd(cmd)
	cmd.Stdin = bytes.NewReader(content)
	stdout := &maxBuf{max: maxStdout}
	stderr := &capBuf{max: 1 << 20}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		msg := tail(stderr.buf.Bytes())
		if msg == "" {
			return nil, fmt.Errorf("stdio parser: %w", err)
		}
		return nil, fmt.Errorf("stdio parser: %w: %s", err, msg)
	}
	chunks, err := decodeChunks(filePath, stdout.buf.Bytes())
	if err != nil {
		return nil, err
	}
	return chunks, nil
}

func (p *StdioParser) Close() error { return nil }
