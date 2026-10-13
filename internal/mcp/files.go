package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func SafePath(root, request string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(request) == "" {
		request = "."
	}
	var joined string
	if filepath.IsAbs(request) {
		joined = filepath.Clean(request)
	} else {
		joined = filepath.Clean(filepath.Join(root, request))
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		lst, lerr := os.Lstat(joined)
		if lerr == nil && lst.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path escapes project root")
		}
		if lerr != nil && !os.IsNotExist(lerr) {
			return "", err
		}
		parent, perr := filepath.EvalSymlinks(filepath.Dir(joined))
		if perr != nil || !within(resolvedRoot, parent) {
			return "", fmt.Errorf("path escapes project root")
		}
		return filepath.Join(parent, filepath.Base(joined)), nil
	}
	if !within(resolvedRoot, resolved) {
		return "", fmt.Errorf("path escapes project root")
	}
	return resolved, nil
}

func within(root, path string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}
