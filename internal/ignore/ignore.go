package ignore

import (
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

type matcher struct {
	dir string
	gi  *gitignore.GitIgnore
}

type Set struct {
	matchers []matcher
}

func Load(root string) (*Set, error) {
	var set Set
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if path != root && (base == ".git" || base == ".onfly") {
				return filepath.SkipDir
			}
			if path != root {
				// Never descend into ignored or blocked trees (node_modules, build output).
				if rel, rerr := filepath.Rel(root, path); rerr == nil && set.Ignored(filepath.ToSlash(rel), true) {
					return filepath.SkipDir
				}
			}
			gi, err := readIgnore(filepath.Join(path, ".gitignore"))
			if err != nil {
				return err
			}
			if gi == nil {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			dir := ""
			if rel != "." {
				dir = filepath.ToSlash(rel)
			}
			set.matchers = append(set.matchers, matcher{dir: dir, gi: gi})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &set, nil
}

func readIgnore(path string) (*gitignore.GitIgnore, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if st.IsDir() {
		return nil, os.ErrInvalid
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(string(body), "\n") {
		if negationLine(line) {
			continue
		}
		lines = append(lines, line)
	}
	return gitignore.CompileIgnoreLines(lines...), nil
}

func negationLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(trimmed, "!") && !strings.HasPrefix(trimmed, `\!`)
}

func (s *Set) Ignored(rel string, isDir bool) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	if rel == "" || rel == "." {
		return false
	}
	if blocked(rel) {
		return true
	}
	if s == nil {
		return false
	}
	for _, m := range s.matchers {
		suffix := rel
		if m.dir != "" {
			prefix := m.dir + "/"
			if rel != m.dir && !strings.HasPrefix(rel, prefix) {
				continue
			}
			if rel == m.dir {
				suffix = ""
			} else {
				suffix = strings.TrimPrefix(rel, prefix)
			}
		}
		if suffix == "" {
			continue
		}
		if m.gi.MatchesPath(suffix) {
			return true
		}
		if isDir && m.gi.MatchesPath(suffix+"/") {
			return true
		}
	}
	return false
}

// Sensitive reports paths the indexer refuses and MCP must not return.
func Sensitive(rel string) bool {
	return blocked(rel)
}

func blocked(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == "" {
			continue
		}
		low := strings.ToLower(part)
		if strings.HasPrefix(low, ".env") || strings.HasPrefix(low, "id_rsa") {
			return true
		}
		for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".keystore"} {
			if strings.HasSuffix(low, suffix) {
				return true
			}
		}
		switch low {
		case "vendor", "node_modules", ".git", ".onfly", ".aws", ".ssh",
			"id_ed25519", "id_ecdsa", "id_dsa", ".npmrc", ".netrc", "credentials.json":
			return true
		}
	}
	return false
}
