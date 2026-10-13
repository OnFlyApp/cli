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
