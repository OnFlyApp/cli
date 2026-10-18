package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/axitdev/onfly/internal/ext"
)

var manifestLanguages = []struct{ file, parser string }{
	{"go.mod", "go"},
	{"composer.json", "php"},
	{"package.json", "typescript"},
	{"Cargo.toml", "rust"},
	{"pyproject.toml", "python"},
	{"requirements.txt", "python"},
	{"Gemfile", "ruby"},
	{"pom.xml", "java"},
	{"build.gradle", "java"},
}

// manifestDepth is how many directories below the project root are searched
// for manifests: a monorepo keeps them under services/*/ or packages/*/.
const manifestDepth = 3

var skipDirs = map[string]bool{"vendor": true, "node_modules": true, ".git": true, ".onfly": true}

func detectLanguages(dir string) []string {
	var out []string
	seen := map[string]bool{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			if skipDirs[d.Name()] || rel != "." && strings.Count(rel, string(filepath.Separator)) >= manifestDepth {
				return filepath.SkipDir
			}
			return nil
		}
		for _, m := range manifestLanguages {
			if d.Name() == m.file {
				seen[m.parser] = true
			}
		}
		return nil
	})
	for _, m := range manifestLanguages {
		if seen[m.parser] && !slices.Contains(out, m.parser) {
			out = append(out, m.parser)
		}
	}
	return out
}

// ensureGitignore adds .onfly/ to the project's .gitignore when the project is
// a git repository (or already has a .gitignore) and does not list it yet.
func ensureGitignore(dir string) (bool, error) {
	path := filepath.Join(dir, ".gitignore")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if os.IsNotExist(err) {
		if _, gerr := os.Stat(filepath.Join(dir, ".git")); gerr != nil {
			return false, nil
		}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		switch strings.TrimSpace(line) {
		case ".onfly", ".onfly/", "/.onfly", "/.onfly/":
			return false, nil
		}
	}
	text := string(raw)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return true, os.WriteFile(path, []byte(text+".onfly/\n"), 0o644)
}

var installParser = func(ctx context.Context, dir, name string) error {
	opt := extOptions(dir)
	opt.Yes = true
	return ext.Install(ctx, opt, name)
}

func bootstrapProject(ctx context.Context, w io.Writer, dir string) {
	if added, err := ensureGitignore(dir); err != nil {
		fmt.Fprintf(w, "could not update .gitignore: %v\n", err)
	} else if added {
		fmt.Fprintln(w, "added .onfly/ to .gitignore")
	}
	for _, lang := range detectLanguages(dir) {
		if err := installParser(ctx, dir, lang); err != nil {
			fmt.Fprintf(w, "no %s parser installed (%v); try: onfly ext install %s\n", lang, err, lang)
			continue
		}
		fmt.Fprintf(w, "installed the %s parser\n", lang)
	}
}
