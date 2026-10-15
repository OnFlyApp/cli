package mcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/config"
)

func TestStartWatchStopsWithContextAndSurvivesFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var running atomic.Bool
	stop := startWatch(context.Background(), config.Config{}, logger, func(ctx context.Context, _ config.Config, _ *slog.Logger) error {
		running.Store(true)
		<-ctx.Done()
		running.Store(false)
		return ctx.Err()
	})
	deadline := time.Now().Add(2 * time.Second)
	for !running.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !running.Load() {
		t.Fatal("watcher never started")
	}
	stop()
	if running.Load() {
		t.Fatal("stop must wait for the watcher to exit")
	}

	failed := make(chan struct{})
	stop = startWatch(context.Background(), config.Config{}, logger, func(context.Context, config.Config, *slog.Logger) error {
		defer close(failed)
		return errors.New("boom")
	})
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("failing watcher did not run")
	}
	stop()
}
