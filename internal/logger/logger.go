// Package logger builds the structured loggers of the program from log/slog.
//
// One rule comes before the others: nothing is ever logged to standard output.
// `onfly serve` speaks JSON-RPC on stdout, and a stray log line there corrupts the
// protocol and crashes the client. Every logger made here writes to a file or to
// standard error; a request for stdout is answered with stderr.
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options chooses the level and the format of a logger.
type Options struct {
	// Level is the lowest level written; the zero value is Info.
	Level slog.Level
	// Verbose lowers the level to Debug.
	Verbose bool
	// JSON writes one JSON object per record, for machines and log shippers.
	JSON bool
	// Plain writes "warning: message key=value" lines, for a terminal. JSON wins.
	Plain bool
	// Clear erases the terminal line before each plain record.
	Clear bool
}

// New returns a logger that writes to w, or to standard error if w is standard
// output.
func New(w io.Writer, o Options) *slog.Logger {
	w = SafeWriter(w)
	level := o.Level
	if o.Verbose {
		level = slog.LevelDebug
	}
	switch {
	case o.JSON:
		return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
	case o.Plain:
		return slog.New(NewPlainHandler(w, level, o.Clear))
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// SafeWriter returns w unless it is standard output, which is never a log
// destination, or nil.
func SafeWriter(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	if f, ok := w.(*os.File); ok && (f == os.Stdout || f.Fd() == os.Stdout.Fd()) {
		return os.Stderr
	}
	return w
}

// ParseLevel reads a level name from the config: debug, info, warn or error.
// Anything else is info.
func ParseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
