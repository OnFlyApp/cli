// Package runner runs a test or build command for an assistant and reduces its
// output to what a developer would read: the failures, with the project files
// and lines they point at. There is no shell: a command is split into words and
// started directly, and only commands the project's config allows are started.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds a run when the config sets none.
	DefaultTimeout = 120 * time.Second
	// MaxTimeout is the longest run a caller may ask for.
	MaxTimeout = 600 * time.Second
	maxOutput  = 8 << 20
)

// Frame is a place in the project that a failure points at.
type Frame struct {
	File string // relative to the project root, slash-separated
	Line int
}

// Failure is one failed test or build error.
type Failure struct {
	Name    string
	Package string
	Message string
	Frames  []Frame
}

// Result is a finished run.
type Result struct {
	Command  string
	Exit     int
	Duration time.Duration
	// Raw is the size of the output the caller would otherwise have read.
	Raw       int
	Summary   string
	Failures  []Failure
	Truncated bool
	TimedOut  bool
}

// Split cuts a command line into words. Quotes group words; there is no
// expansion, and shell operators are an error because nothing would interpret them.
func Split(cmd string) ([]string, error) {
	var words []string
	var cur strings.Builder
	var quote rune
	started := false
	for _, r := range cmd {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, started = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if started || cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
				started = false
			}
		case strings.ContainsRune(";|&<>`$()", r):
			return nil, fmt.Errorf("shell syntax (%q) is not supported: the command is started directly, without a shell", string(r))
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if started || cur.Len() > 0 {
		words = append(words, cur.String())
	}
	if len(words) == 0 {
		return nil, errors.New("empty command")
	}
	return words, nil
}

// Allowed reports whether the words start with one of the allowlisted commands,
// word for word: "go test" allows "go test ./..." but not "go build" or "gotest".
func Allowed(words []string, allow []string) bool {
	for _, a := range allow {
		aw, err := Split(a)
		if err != nil || len(aw) > len(words) {
			continue
		}
		ok := true
		for i := range aw {
			if aw[i] != words[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// Run starts the command in root and parses its output. root is the project
// directory; a first word with a slash is taken relative to it.
func Run(ctx context.Context, root string, words, allow []string, timeout time.Duration) (Result, error) {
	if len(allow) == 0 {
		return Result{}, errors.New("no command is allowed: list the commands in run.commands in .onfly/config.yaml")
	}
	if !Allowed(words, allow) {
		return Result{}, fmt.Errorf("%q is not in run.commands (allowed: %s)", strings.Join(words, " "), strings.Join(allow, ", "))
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timeout = min(timeout, MaxTimeout)
	argv := withJSON(words)
	bin := argv[0]
	if strings.Contains(bin, "/") && !filepath.IsAbs(bin) {
		bin = filepath.Join(root, bin)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "CI=1", "TERM=dumb")
	cmd.WaitDelay = 5 * time.Second
	out := &capped{max: maxOutput}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err := cmd.Run()
	res := Result{Command: strings.Join(words, " "), Duration: time.Since(start), Raw: out.total, Truncated: out.truncated, TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.Exit = exit.ExitCode()
	default:
		return res, err
	}
	parse(&res, words, out.buf.Bytes(), root)
	return res, nil
}

// withJSON asks `go test` for its event stream, which says exactly which test
// failed and what it printed.
func withJSON(words []string) []string {
	if len(words) >= 2 && words[0] == "go" && words[1] == "test" {
		for _, w := range words {
			if w == "-json" {
				return words
			}
		}
		return append([]string{"go", "test", "-json"}, words[2:]...)
	}
	return words
}

type capped struct {
	buf       bytes.Buffer
	max       int
	total     int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.total += len(p)
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.truncated = true
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	} else {
		c.truncated = true
	}
	return len(p), nil
}
