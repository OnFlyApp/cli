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

type Commit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

const logFormat = "--format=%h%x09%an%x09%ad%x09%s"

func LineLog(ctx context.Context, root, file string, start, end, n int) ([]Commit, error) {
	if file == "" || strings.HasPrefix(file, "-") || start < 1 || end < start {
		return nil, fmt.Errorf("invalid history range %q:%d-%d", file, start, end)
	}
	lines, err := run(ctx, root, "log", "-L"+strconv.Itoa(start)+","+strconv.Itoa(end)+":"+file,
		"-n", strconv.Itoa(n), "-s", "--date=short", logFormat)
	if err != nil {
		return nil, err
	}
	return commits(lines), nil
}

func FileLog(ctx context.Context, root, file string, n int) ([]Commit, error) {
	if file == "" || strings.HasPrefix(file, "-") {
		return nil, fmt.Errorf("invalid file %q", file)
	}
	lines, err := run(ctx, root, "log", "-n", strconv.Itoa(n), "--date=short", logFormat, "--", file)
	if err != nil {
		return nil, err
	}
	return commits(lines), nil
}

func commits(lines []string) []Commit {
	out := []Commit{}
	for _, l := range lines {
		f := strings.SplitN(l, "\t", 4)
		if len(f) == 4 {
			out = append(out, Commit{Hash: f[0], Author: f[1], Date: f[2], Subject: f[3]})
		}
	}
	return out
}

// Diff is `git diff --unified=0` of the working tree against ref (HEAD when
// empty), or of the index when staged. Untracked files are not part of it.
func Diff(ctx context.Context, root, ref string, staged bool) (string, error) {
	args := []string{"diff", "--unified=0", "--no-color", "--no-ext-diff"}
	if staged {
		args = append(args, "--cached")
	}
	if ref != "" {
		if strings.HasPrefix(ref, "-") {
			return "", fmt.Errorf("invalid git ref %q", ref)
		}
		args = append(args, ref)
	} else if !staged {
		args = append(args, "HEAD")
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Tracked lists the files git tracks, relative to root.
func Tracked(ctx context.Context, root string) ([]string, error) {
	return run(ctx, root, "ls-files")
}
