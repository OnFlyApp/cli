package vectorstore

import (
	"context"
	"errors"

	"github.com/axitdev/onfly/internal/parser"
)

type EmbeddedChunk struct {
	Chunk     parser.Chunk
	Vector    []float32
	Workspace string
}

var (
	ErrNotFound          = errors.New("vectorstore: not found")
	ErrDimensionMismatch = errors.New("vectorstore: dimension mismatch")
)

type StoredChunk struct {
	ID    int64
	Chunk parser.Chunk
}

type Store interface {
	Init(ctx context.Context, dimensions int) error
	SaveChunks(ctx context.Context, chunks []EmbeddedChunk) error
	CommitFile(ctx context.Context, path, hash string, chunks []EmbeddedChunk) error
	DeleteFiles(ctx context.Context, filePaths []string) error
	ListFiles(ctx context.Context) ([]string, error)
	ListChunks(ctx context.Context) ([]StoredChunk, error)
	ChunksByFiles(ctx context.Context, paths []string) ([]StoredChunk, error)
	Names(ctx context.Context) ([]string, error)
	UpdateOutbound(ctx context.Context, outbound map[int64][]string) error
	SearchSemantic(ctx context.Context, queryVector []float32, limit int, workspace string) ([]parser.Chunk, error)
	SearchKeyword(ctx context.Context, keyword string, limit int, workspace string) ([]parser.Chunk, error)
	GetComponentAST(ctx context.Context, name string) (*parser.Chunk, error)
	GetReferences(ctx context.Context, name string) ([]parser.Chunk, error)
	WorkspacesForName(ctx context.Context, name string) ([]string, error)
	FileHash(ctx context.Context, path string) (string, bool, error)
	FileHashes(ctx context.Context) (map[string]string, error)
	Generation(ctx context.Context) (int64, error)
	Optimize(ctx context.Context) error
	Close() error
}
