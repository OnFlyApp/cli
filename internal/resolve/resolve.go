// Package resolve decides whether a name used inside one chunk can refer to a
// given definition. The index stores calls as bare names ("Split"), so without
// this every Split in the repository would match every call to strings.Split.
// The rules are those of the language, applied to the caller's own text and
// imports; no type information is needed.
package resolve

import (
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/axitdev/onfly/internal/parser"
)

// Accepts reports whether caller, whose file has the text src, can be using
// def by the name def carries. When src is empty (the file is gone) or the
// language has no rules here, it accepts.
func Accepts(src string, caller, def parser.Chunk) bool {
	return Rules{}.Accepts(src, caller, def, 0)
}

// Rules are the settings of the resolver. The zero value is the defaults.
type Rules struct {
	// MaxUnambiguous is how many definitions a method name may have in the whole
	// project for a call on a value of unknown type to be attributed to it: a
	// name only one class defines cannot be anything else. 0 means the default (1);
	// a negative value turns the rule off.
	MaxUnambiguous int
	// SelfQualifiers are the member-access prefixes that mean "this class" (PHP:
	// $this, self, static, parent). Empty means the defaults.
	SelfQualifiers []string
}

// DefaultSelfQualifiers are the PHP prefixes that name the class itself.
var DefaultSelfQualifiers = []string{"$this", "self", "static", "parent"}

// DefaultMaxUnambiguous is Rules.MaxUnambiguous when it is not set.
const DefaultMaxUnambiguous = 1

func (r Rules) maxUnambiguous() int {
	if r.MaxUnambiguous == 0 {
		return DefaultMaxUnambiguous
	}
	return r.MaxUnambiguous
}

func (r Rules) isSelf(qual string) bool {
	list := r.SelfQualifiers
	if len(list) == 0 {
		list = DefaultSelfQualifiers
	}
	return slices.Contains(list, qual)
}

// Accepts is the package function with the rules, and with the number of
// definitions the project has under def's name (0 when it is not known).
func (r Rules) Accepts(src string, caller, def parser.Chunk, siblings int) bool {
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
		return r.phpAccepts(src, caller, def, name, siblings)
	}
	return true
}

// Methods that many unrelated types implement because a standard interface or
// the language asks for them. A call to one on an untyped value is only
// attributed to a definition in the caller's own file.
var (
	goUbiquitous = names("String", "Error", "Read", "Write", "Close", "Len", "Less", "Swap",
		"MarshalJSON", "UnmarshalJSON", "MarshalText", "UnmarshalText", "Scan", "Value")
	phpUbiquitous = names("__construct", "__destruct", "__toString", "__invoke", "__get", "__set",
		"toArray", "jsonSerialize", "handle")
)

func names(list ...string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, n := range list {
		m[n] = true
	}
	return m
}

type phpCall struct {
	qual  string
	isNew bool
	arrow bool // after ->: a call on a value, whatever the qualifier text
}

var (
	phpMatchCache = map[string][]phpCall{}
)

// phpUsesOf lists how a chunk's text uses a name: after which qualifier, and
// whether with new. One chunk is asked about the same name once per candidate
// definition, so the answer is kept.
func phpUsesOf(name, content string) []phpCall {
	key := name + "\x00" + content
	cacheMu.Lock()
	if m, ok := phpMatchCache[key]; ok {
		cacheMu.Unlock()
		return m
	}
	cacheMu.Unlock()
	re := cachedRegexp("php|"+name, func() string {
		return `(?:(\$?[\\\w]+)\s*(::|->|\?->)\s*)?(\bnew\s+)?\\?\b` + regexp.QuoteMeta(name) + `\b`
	})
	var out []phpCall
	for _, m := range re.FindAllStringSubmatchIndex(content, -1) {
		u := phpCall{isNew: m[6] >= 0}
		if m[2] >= 0 {
			u.qual = content[m[2]:m[3]]
			u.arrow = strings.HasSuffix(content[m[4]:m[5]], "->")
		}
		out = append(out, u)
	}
	cacheMu.Lock()
	if len(phpMatchCache) >= cacheLimit*16 {
		phpMatchCache = map[string][]phpCall{}
	}
	phpMatchCache[key] = out
	cacheMu.Unlock()
	return out
}

// Resolving one file's calls asks the same questions about the same source and
// the same names thousands of times, so the parsed imports and the compiled
// patterns are kept. The caches are cleared when they grow, so a long-running
// server does not hold on to every file it has seen.
const cacheLimit = 256

var (
	cacheMu     sync.Mutex
	reCache     = map[string]*regexp.Regexp{}
	useCache    = map[string]map[string]string{}
	importCache = map[string]map[string]string{}
)

func cachedRegexp(key string, pattern func() string) *regexp.Regexp {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if re, ok := reCache[key]; ok {
		return re
	}
	if len(reCache) >= cacheLimit*8 {
		reCache = map[string]*regexp.Regexp{}
	}
	re := regexp.MustCompile(pattern())
	reCache[key] = re
	return re
}

func cachedUses(src string) map[string]string {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if m, ok := useCache[src]; ok {
		return m
	}
	if len(useCache) >= cacheLimit {
		useCache = map[string]map[string]string{}
	}
	m := phpUses(src)
	useCache[src] = m
	return m
}

func cachedImports(src string) map[string]string {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if m, ok := importCache[src]; ok {
		return m
	}
	if len(importCache) >= cacheLimit {
		importCache = map[string]map[string]string{}
	}
	m := goImports(src)
	importCache[src] = m
	return m
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

type goCall struct {
	qual  string
	paren bool // the name is followed by "("
}

var goMatchCache = map[string][]goCall{}

// goUsesOf lists how a chunk's text uses a name: after which qualifier, and
// whether it is called. One chunk is asked once per candidate definition.
func goUsesOf(name, content string) []goCall {
	key := name + "\x00" + content
	cacheMu.Lock()
	if m, ok := goMatchCache[key]; ok {
		cacheMu.Unlock()
		return m
	}
	cacheMu.Unlock()
	re := cachedRegexp("go|"+name, func() string { return `(?:\b([A-Za-z_]\w*)\s*\.\s*)?\b` + regexp.QuoteMeta(name) + `\b` })
	var out []goCall
	for _, m := range re.FindAllStringSubmatchIndex(content, -1) {
		c := goCall{paren: strings.HasPrefix(strings.TrimLeft(content[m[1]:], " \t"), "(")}
		if m[2] >= 0 {
			c.qual = content[m[2]:m[3]]
		}
		out = append(out, c)
	}
	cacheMu.Lock()
	if len(goMatchCache) >= cacheLimit*16 {
		goMatchCache = map[string][]goCall{}
	}
	goMatchCache[key] = out
	cacheMu.Unlock()
	return out
}

func goAccepts(src string, caller, def parser.Chunk, name string) bool {
	if !strings.Contains(caller.Content, name) {
		return false
	}
	imports := cachedImports(src)
	isMethod := def.Metadata["receiver"] != ""
	for _, m := range goUsesOf(name, caller.Content) {
		q := m.qual
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
			// A call on a value of unknown type: the method can be def's only if
			// the caller lives in def's package or imports it. A method of an
			// unrelated package that shares the name is not a caller.
			if isMethod && m.paren && (!goUbiquitous[name] || def.FilePath == caller.FilePath) &&
				(path.Dir(caller.FilePath) == path.Dir(def.FilePath) || importsAnyDir(imports, path.Dir(def.FilePath))) {
				return true
			}
		}
	}
	return false
}

func importsAnyDir(imports map[string]string, dir string) bool {
	for _, p := range imports {
		if importsDir(p, dir) {
			return true
		}
	}
	return false
}

var (
	phpUse = regexp.MustCompile(`(?m)^\s*use\s+([\\\w]+)(?:\s+as\s+(\w+))?\s*;`)
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

// phpRelated says whether a method called on a value of unknown type can
// belong to def's class: the class is the caller's own, one it imports (a
// parent, a trait, an interface, a collaborator), or lives in its namespace. A
// method that merely shares its name with one of an unrelated class is not.
func phpRelated(def, caller parser.Chunk, uses map[string]string, ns string) bool {
	if def.FilePath == caller.FilePath {
		return true
	}
	class := def.Metadata["receiver"]
	for _, full := range uses {
		if full == class {
			return true
		}
	}
	return ns != "" && def.Metadata["namespace"] == ns
}

var propTypeCache = map[string]map[string][]string{}

// phpPropertyTypes lists the class names the file gives to $this->prop: a
// docblock @var, a typed or promoted property, a constructor parameter of that
// name, or the class an assignment makes or resolves (new Foo, Foo::class).
// None means the file does not say.
func phpPropertyTypes(src, prop string) []string {
	cacheMu.Lock()
	t, ok := propTypeCache[src][prop]
	cacheMu.Unlock()
	if ok {
		return t
	}
	q := regexp.QuoteMeta(prop)
	var out []string
	for _, pat := range []string{
		`@var\s+\??([\\\w]+)[^\n]*\n(?:\s*\*[^\n]*\n)*\s*\*/\s*\n\s*(?:(?:public|protected|private|static|readonly)\s+)+[^\n$;]*\$` + q + `\b`,
		`\??([A-Z\\][\\\w]*)\s+\$` + q + `\b`,
		`\$this->` + q + `\s*=[^;]*?(?:\bnew\s+\\?([\\\w]+)|([\\\w]+)::class)`,
	} {
		re := cachedRegexp("phpprop|"+pat, func() string { return pat })
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			for _, g := range m[1:] {
				if g != "" {
					out = append(out, g)
				}
			}
		}
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if len(propTypeCache) >= cacheLimit {
		propTypeCache = map[string]map[string][]string{}
	}
	if propTypeCache[src] == nil {
		propTypeCache[src] = map[string][]string{}
	}
	propTypeCache[src][prop] = out
	return out
}

func (r Rules) phpAccepts(src string, caller, def parser.Chunk, name string, siblings int) bool {
	uses := cachedUses(src)
	ns := caller.Metadata["namespace"]
	defClass := def.Metadata["receiver"]
	isMethod := defClass != ""
	classLike := !isMethod && def.Metadata["fqn"] != ""
	if !strings.Contains(caller.Content, name) {
		return false
	}
	for _, m := range phpUsesOf(name, caller.Content) {
		qual, isNew := m.qual, m.isNew
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
		case strings.HasPrefix(qual, "$") || r.isSelf(qual) || m.arrow:
			isVar := strings.HasPrefix(qual, "$") && !r.isSelf(qual)
			// $this->limiter->tooManyAttempts(): when the file says what the property
			// holds, that class decides, not a neighbour in the same namespace.
			if types := phpPropertyTypes(src, strings.TrimPrefix(qual, "$")); (m.arrow || isVar) && !r.isSelf(qual) && len(types) > 0 && (isVar || !strings.HasPrefix(qual, "$")) {
				if isMethod {
					for _, t := range types {
						if phpClass(t, uses, ns) == defClass {
							return true
						}
					}
				}
				continue
			}
			if !isMethod {
				continue
			}
			// $cashbackCycle->clawBackCashback(): a variable named for the class.
			if isVar && sameClassName(qual[1:], defClass) {
				return true
			}
			if phpRelated(def, caller, uses, ns) && (!phpUbiquitous[name] || def.FilePath == caller.FilePath) {
				return true
			}
			// A name that only one class in the project defines is that class's.
			if limit := r.maxUnambiguous(); limit > 0 && siblings > 0 && siblings <= limit && !phpUbiquitous[name] {
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

// sameClassName says that a variable is named for a class: $cashbackCycle for
// App\Cashback\CashbackCycle.
func sameClassName(variable, class string) bool {
	if i := strings.LastIndexAny(class, `\.`); i >= 0 {
		class = class[i+1:]
	}
	return class != "" && strings.EqualFold(variable, class)
}
