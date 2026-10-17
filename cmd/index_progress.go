package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/axitdev/onfly/internal/index"
	"github.com/axitdev/onfly/internal/logger"
)

const progressWidth = 20

type indexProgress struct {
	out     io.Writer
	tty     bool
	perFile bool
	log     *slog.Logger
}

func newIndexProgress(out *os.File, perFile bool) *indexProgress {
	return &indexProgress{out: out, tty: isTerminal(out), perFile: perFile}
}

func (p *indexProgress) Event(ev index.Event) {
	if jsonLogs {
		// A progress bar or a plain line would break a stream of JSON records.
		level := slog.LevelInfo
		if (ev.Action == "parsing" || ev.Action == "summarising" || ev.Action == "unchanged") && ev.Total > 0 && !p.perFile {
			level = slog.LevelDebug
		}
		p.logger().Log(context.Background(), level, ev.Action, "n", ev.N, "total", ev.Total, "path", ev.Path)
		return
	}
	if !p.tty && !p.perFile && (ev.Action == "parsing" || ev.Action == "summarising") && ev.Total > 0 {
		return
	}
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
	if p.log == nil {
		p.log = logger.New(p.out, logger.Options{Verbose: verbose, JSON: jsonLogs, Plain: true, Clear: p.tty && !jsonLogs})
	}
	return p.log
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
