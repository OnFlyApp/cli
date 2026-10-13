package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/axitdev/onfly/internal/index"
)

const progressWidth = 20

type indexProgress struct {
	out io.Writer
	tty bool
}

func newIndexProgress(out *os.File) *indexProgress {
	st, err := out.Stat()
	tty := err == nil && st.Mode()&os.ModeCharDevice != 0
	return &indexProgress{out: out, tty: tty}
}

func (p *indexProgress) Event(ev index.Event) {
	line := formatIndexProgress(ev, p.tty)
	if p.tty {
		fmt.Fprintf(p.out, "\r\033[K%s", line)
		return
	}
	fmt.Fprintln(p.out, line)
}

func (p *indexProgress) Finish() {
	if p.tty {
		fmt.Fprintln(p.out)
	}
}

func (p *indexProgress) logger() *slog.Logger {
	next := slog.NewTextHandler(p.out, nil)
	if !p.tty {
		return slog.New(next)
	}
	return slog.New(clearLineHandler{next: next, out: p.out})
}

func formatIndexProgress(ev index.Event, tty bool) string {
	line := ev.Action
	if ev.Total > 0 {
		line = fmt.Sprintf("%d/%d %s", ev.N, ev.Total, ev.Action)
		if tty {
			filled := progressWidth * ev.N / ev.Total
			if filled > progressWidth {
				filled = progressWidth
			}
			if filled < 0 {
				filled = 0
			}
			bar := strings.Repeat("#", filled) + strings.Repeat("-", progressWidth-filled)
			line = "[" + bar + "] " + line
		}
	}
	if ev.Path != "" {
		line += " " + ev.Path
	}
	if tty && len(line) > 100 {
		line = line[:97] + "..."
	}
	return line
}

// clearLineHandler erases a terminal progress line before a log record.
type clearLineHandler struct {
	next slog.Handler
	out  io.Writer
}

func (h clearLineHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h clearLineHandler) Handle(ctx context.Context, rec slog.Record) error {
	fmt.Fprint(h.out, "\r\033[K")
	return h.next.Handle(ctx, rec)
}

func (h clearLineHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return clearLineHandler{next: h.next.WithAttrs(attrs), out: h.out}
}

func (h clearLineHandler) WithGroup(name string) slog.Handler {
	return clearLineHandler{next: h.next.WithGroup(name), out: h.out}
}
