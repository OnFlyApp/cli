// Package resolve decides whether a name used inside one chunk can refer to a
// given definition. The index stores calls as bare names ("Split"), so without
// this every Split in the repository would match every call to strings.Split.
// The rules are those of the language, applied to the caller's own text and
// imports; no type information is needed.
package resolve

import (
	"path"
	"regexp"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
)

// Accepts reports whether caller, whose file has the text src, can be using
// def by the name def carries. When src is empty (the file is gone) or the
// language has no rules here, it accepts.
func Accepts(src string, caller, def parser.Chunk) bool {
	if src == "" {
		return true
	}
	name := def.Metadata["name"]
	if name == "" {
		return true
	}
	switch strings.ToLower(path.Ext(caller.FilePath)) {
	case ".go":
		return goAccepts(src, caller, def, name)
	case ".php":
		return phpAccepts(src, caller, def, name)
	}
	return true
}

var (
	goImportLine = regexp.MustCompile(`^\s*(?:import\s+)?([A-Za-z_][\w]*|\.)?\s*"([^"]+)"\s*$`)
	goVersion    = regexp.MustCompile(`^v\d+$`)
)

// goImports maps the name each import is used under to its path.
func goImports(src string) map[string]string {
	out := map[string]string{}
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "import ("):
			inBlock = true
			continue
		case inBlock && t == ")":
			inBlock = false
			continue
		case !inBlock && !strings.HasPrefix(t, "import "):
			continue
		}
		m := goImportLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		alias, p := m[1], m[2]
		if alias == "_" || alias == "." {
			continue
		}
		if alias == "" {
			parts := strings.Split(p, "/")
			alias = parts[len(parts)-1]
			if goVersion.MatchString(alias) && len(parts) > 1 {
				alias = parts[len(parts)-2]
			}
		}
		out[alias] = p
	}
	return out
}

// importsDir reports whether import path p names the package in dir (a
// project-relative directory), e.g. ".../onfly/internal/skeleton" and
// "internal/skeleton". Standard library paths never end in a project directory.
func importsDir(p, dir string) bool {
	if dir == "." || dir == "" {
		return false
	}
	return p == dir || strings.HasSuffix(p, "/"+dir)
}

func goAccepts(src string, caller, def parser.Chunk, name string) bool {
	imports := goImports(src)
	use := regexp.MustCompile(`(?:\b([A-Za-z_]\w*)\s*\.\s*)?\b` + regexp.QuoteMeta(name) + `\b`)
	for _, m := range use.FindAllStringSubmatchIndex(caller.Content, -1) {
		q := ""
		if m[2] >= 0 {
			q = caller.Content[m[2]:m[3]]
		}
		isMethod := def.Metadata["receiver"] != ""
		switch {
		case q == "":
			if !isMethod && path.Dir(caller.FilePath) == path.Dir(def.FilePath) {
				return true
			}
		case imports[q] != "":
			if !isMethod && importsDir(imports[q], path.Dir(def.FilePath)) {
				return true
			}
		default:
			rest := strings.TrimLeft(caller.Content[m[1]:], " \t")
			if isMethod && strings.HasPrefix(rest, "(") {
				return true
			}
		}
	}
	return false
}

var (
	phpUse    = regexp.MustCompile(`(?m)^\s*use\s+([\\\w]+)(?:\s+as\s+(\w+))?\s*;`)
	phpMember = regexp.MustCompile(`(\$?[\\\w]+)\s*(?:::|->|\?->)\s*`)
)

// phpUses maps the short name of each imported class to its qualified name.
func phpUses(src string) map[string]string {
	out := map[string]string{}
	for _, m := range phpUse.FindAllStringSubmatch(src, -1) {
		full := strings.TrimPrefix(m[1], `\`)
		alias := m[2]
		if alias == "" {
			parts := strings.Split(full, `\`)
			alias = parts[len(parts)-1]
		}
		out[alias] = full
	}
	return out
}

func phpClass(q string, uses map[string]string, ns string) string {
	if strings.HasPrefix(q, `\`) {
		return strings.TrimPrefix(q, `\`)
	}
	head, rest, _ := strings.Cut(q, `\`)
	if full, ok := uses[head]; ok {
		if rest != "" {
			return full + `\` + rest
		}
		return full
	}
	if ns == "" {
		return q
	}
	return ns + `\` + q
}

func phpAccepts(src string, caller, def parser.Chunk, name string) bool {
	uses := phpUses(src)
	ns := caller.Metadata["namespace"]
	defClass := def.Metadata["receiver"]
	isMethod := defClass != ""
	classLike := !isMethod && def.Metadata["fqn"] != ""
	use := regexp.MustCompile(`(?:(\$?[\\\w]+)\s*(::|->|\?->)\s*)?(\bnew\s+)?\\?\b` + regexp.QuoteMeta(name) + `\b`)
	for _, m := range use.FindAllStringSubmatchIndex(caller.Content, -1) {
		qual, isNew := "", m[6] >= 0
		if m[2] >= 0 {
			qual = caller.Content[m[2]:m[3]]
		}
		switch {
		case qual == "" && isNew:
			if classLike && phpClass(name, uses, ns) == def.Metadata["fqn"] {
				return true
			}
		case qual == "":
			switch {
			case classLike && phpClass(name, uses, ns) == def.Metadata["fqn"]:
				return true
			case !isMethod && !classLike && def.Metadata["namespace"] == ns:
				return true
			}
		case strings.HasPrefix(qual, "$") || qual == "self" || qual == "static" || qual == "parent":
			if isMethod {
				return true
			}
		default:
			if isMethod && phpClass(qual, uses, ns) == defClass {
				return true
			}
		}
	}
	return false
}
