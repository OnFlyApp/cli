package vectorstore

import (
	"context"
	"fmt"

	"github.com/axitdev/onfly/internal/config"
)

func New(ctx context.Context, cfg config.Database) (Store, error) {
	switch cfg.Driver {
	case "sqlite":
		return OpenSQLite(cfg.DSN)
	case "postgres":
		return OpenPostgres(ctx, cfg.DSN)
	default:
		return nil, fmt.Errorf("database driver %q is not available", cfg.Driver)
	}
}

func InitAll(ctx context.Context, st Store, cfg config.Config) error {
	if err := st.Init(ctx, cfg.Embedder.Dimensions); err != nil {
		return err
	}
	for slot, dims := range cfg.ExtraSlots() {
		if err := st.InitExtra(ctx, slot, dims); err != nil {
			return fmt.Errorf("extra vectors %q: %w", slot, err)
		}
	}
	return nil
}

type Checker interface {
	CheckMeta(ctx context.Context, dimensions int) error
	Ping(ctx context.Context) error
	UnnamedChunks(ctx context.Context) (int, error)
	Stats(ctx context.Context) (IndexStats, error)
	Close() error
}

func OpenChecker(ctx context.Context, cfg config.Database) (Checker, error) {
	st, err := New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return st.(Checker), nil
}
