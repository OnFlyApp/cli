package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type Chunk struct {
	FilePath string            `json:"file_path"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata"`
	Outbound []string          `json:"outbound,omitempty"`
}

type Parser interface {
	Parse(ctx context.Context, filePath string, content []byte) ([]Chunk, error)
	Close() error
}

func decodeChunks(filePath string, stdout []byte) ([]Chunk, error) {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return []Chunk{}, nil
	}
	var chunks []Chunk
	if err := json.Unmarshal(stdout, &chunks); err != nil {
		return nil, fmt.Errorf("parse plugin json: %w", err)
	}
	if chunks == nil {
		chunks = []Chunk{}
	}
	for i := range chunks {
		if chunks[i].FilePath == "" {
			chunks[i].FilePath = filePath
		}
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = map[string]string{}
		}
	}
	return chunks, nil
}

func tail(b []byte) string {
	const n = 4096
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

const maxStdout = 32 << 20

type maxBuf struct {
	buf bytes.Buffer
	max int
	err error
}

func (m *maxBuf) Write(p []byte) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	if m.buf.Len()+len(p) > m.max {
		m.err = fmt.Errorf("parser stdout exceeds %d bytes", m.max)
		return 0, m.err
	}
	return m.buf.Write(p)
}

type capBuf struct {
	buf bytes.Buffer
	max int
}

func (c *capBuf) Write(p []byte) (int, error) {
	if c.buf.Len() < c.max {
		remain := c.max - c.buf.Len()
		chunk := p
		if len(chunk) > remain {
			chunk = chunk[:remain]
		}
		_, _ = c.buf.Write(chunk)
	}
	return len(p), nil
}
