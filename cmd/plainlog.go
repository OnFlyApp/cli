package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// plainHandler writes log records as "warning: message key=value" lines, with
// no timestamp, so they read well between progress lines. A terminal progress
// line is erased first when clearLine is set.
type plainHandler struct {
	w         io.Writer
	mu        *sync.Mutex
	attrs     []slog.Attr
	min       slog.Level
	clearLine bool
}

func newPlainHandler(w io.Writer, min slog.Level, clearLine bool) *plainHandler {
	return &plainHandler{w: w, mu: &sync.Mutex{}, min: min, clearLine: clearLine}
}

func (h *plainHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.min }

func (h *plainHandler) Handle(_ context.Context, rec slog.Record) error {
	prefix := ""
	switch {
	case rec.Level >= slog.LevelError:
		prefix = "error: "
	case rec.Level >= slog.LevelWarn:
		prefix = "warning: "
	}
	line := prefix + rec.Message
	add := func(a slog.Attr) {
		line += fmt.Sprintf(" %s=%v", a.Key, a.Value)
	}
	for _, a := range h.attrs {
		add(a)
	}
	rec.Attrs(func(a slog.Attr) bool { add(a); return true })
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clearLine {
		fmt.Fprint(h.w, "\r\033[K")
	}
	_, err := fmt.Fprintln(h.w, line)
	return err
}

func (h *plainHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &next
}

func (h *plainHandler) WithGroup(string) slog.Handler { return h }
