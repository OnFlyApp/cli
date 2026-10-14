// Package gitx asks git which files changed. Paths are relative to root, slash-separated.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func run(ctx context.Context, root string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	var lines []string
	for _, l := range strings.Split(out.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// ChangedSince lists files that differ from ref in the working tree, plus untracked files.
func ChangedSince(ctx context.Context, root, ref string) ([]string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return nil, fmt.Errorf("invalid git ref %q", ref)
	}
	changed, err := run(ctx, root, "diff", "--name-only", "--relative", ref, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := run(ctx, root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return dedupe(append(changed, untracked...)), nil
}

// Recent lists files committed in the last days days, most recently changed first.
func Recent(ctx context.Context, root string, days int) ([]string, error) {
	if days < 1 {
		days = 30
	}
	files, err := run(ctx, root, "log", "--since="+strconv.Itoa(days)+".days", "--relative", "--name-only", "--pretty=format:")
	if err != nil {
		return nil, err
	}
	return dedupe(files), nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, f := range in {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
