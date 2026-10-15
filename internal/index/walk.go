package index

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/ignore"
	"github.com/bmatcuk/doublestar/v4"
)

func walk(root string, include []string, filter *ignore.Filter, want func(string) bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, evalErr := filepath.EvalSymlinks(path)
			info, statErr := os.Stat(path)
			if evalErr != nil || statErr != nil || info.IsDir() || !withinRoot(root, target) || !linkTargetOK(root, target, filter) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = slash(rel)
		if rel == "." {
			return nil
		}
		if filter.Skip(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ok, err := matchAny(rel, include)
		if err != nil {
			return err
		}
		if !ok || !want(rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func slash(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), `\`, "/")
}

func resolvedRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return filepath.Clean(root)
}

func withinRoot(root, path string) bool {
	root = resolvedRoot(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func linkTargetOK(root, target string, filter *ignore.Filter) bool {
	root = resolvedRoot(root)
	rel, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return false
	}
	return !filter.Skip(slash(rel), false)
}

func matchAny(rel string, patterns []string) (bool, error) {
	for _, pattern := range patterns {
		ok, err := doublestar.Match(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("indexer include %q: %w", pattern, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
