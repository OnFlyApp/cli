package resolve

import (
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func ch(file, content string, meta map[string]string) parser.Chunk {
	return parser.Chunk{FilePath: file, Content: content, Metadata: meta}
}

const goSrc = `package skeleton

import (
	"fmt"
	"strings"

	"github.com/axitdev/onfly/internal/tokens"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/acme/lib/v2"
)
`

func TestGoResolution(t *testing.T) {
	fn := func(file, name string) parser.Chunk { return ch(file, "", map[string]string{"name": name}) }
	method := func(file, name, recv string) parser.Chunk {
		return ch(file, "", map[string]string{"name": name, "receiver": recv})
	}
	tests := []struct {
		name    string
		content string
		def     parser.Chunk
		want    bool
	}{
		{"strings.Split is not the repo's Split", "parts := strings.Split(s, \",\")", fn("internal/eval/eval.go", "Split"), false},
		{"imported package function", "n, _ := tokens.Count(s)", fn("internal/tokens/tokens.go", "Count"), true},
		{"imported under an alias", "sdkmcp.AddTool(srv)", fn("vendor/x/mcp/mcp.go", "AddTool"), false},
		{"bare call stays in the package", "return indentOf(line)", fn("internal/skeleton/skeleton.go", "indentOf"), true},
		{"bare call does not cross packages", "return indentOf(line)", fn("internal/other/x.go", "indentOf"), false},
		{"method call on a value", "h.store.GetComponents(ctx, n)", method("internal/vectorstore/postgres.go", "GetComponents", "Postgres"), true},
		{"predeclared append is not a method", "out = append(out, x)", method("internal/usage/usage.go", "append", "Log"), false},
		{"a field read is not a method call", "srcs.text[rel] = src", method("internal/rules/rules.go", "text", "Rule"), false},
		{"String on a value does not cross files", "b.String()", method("internal/rules/rules.go", "String", "parserNames"), false},
		{"String on a value in the same file", "b.String()", method("internal/skeleton/skeleton.go", "String", "scanner"), true},
		{"a domain method on a value still resolves", "h.record(x)", method("internal/mcp/server.go", "record", "tools"), true},
		{"a function is not called through a value", "x.Count()", fn("internal/tokens/tokens.go", "Count"), false},
		{"name not in the text", "return 1", fn("internal/skeleton/skeleton.go", "Collapse"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caller := ch("internal/skeleton/skeleton.go", tc.content, map[string]string{"name": "F"})
			if got := Accepts(goSrc, caller, tc.def); got != tc.want {
				t.Fatalf("Accepts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGoImportNames(t *testing.T) {
	got := goImports(goSrc + "import single \"example.com/one/two\"\n")
	want := map[string]string{
		"fmt": "fmt", "strings": "strings", "tokens": "github.com/axitdev/onfly/internal/tokens",
		"sdkmcp": "github.com/modelcontextprotocol/go-sdk/mcp", "lib": "github.com/acme/lib/v2", "single": "example.com/one/two",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("import %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestAcceptsEverythingWithoutSource(t *testing.T) {
	def := ch("a/b.go", "", map[string]string{"name": "Split"})
	if !Accepts("", ch("c/d.go", "strings.Split(x)", nil), def) {
		t.Fatal("an unreadable caller file must not hide a candidate")
	}
	if !Accepts("x", ch("c/d.rb", "Split(x)", nil), def) {
		t.Fatal("languages without rules accept")
	}
}

const phpSrc = `<?php
namespace App\Http;

use App\Models\Story;
use App\Support\Money as Cash;
`

func TestPHPResolution(t *testing.T) {
	story := ch("app/Models/Story.php", "", map[string]string{"name": "Story", "fqn": `App\Models\Story`, "namespace": `App\Models`})
	method := func(name, recv string) parser.Chunk {
		return ch("x.php", "", map[string]string{"name": name, "receiver": recv, "namespace": "N"})
	}
	tests := []struct {
		name    string
		content string
		def     parser.Chunk
		want    bool
	}{
		{"new of an imported class", "$s = new Story();", story, true},
		{"static call on an imported class", "Story::query()", method("query", `App\Models\Story`), true},
		{"static call on a different class of that name", "Story::query()", method("query", `Other\Story`), false},
		{"aliased import", "Cash::format(1)", method("format", `App\Support\Money`), true},
		{"method on a variable", "$this->save()", method("save", `App\Models\Story`), true},
		{"a method is not called bare", "save()", method("save", `App\Models\Story`), false},
		{"handle() on a variable does not cross files", "$job->handle()", method("handle", `App\Jobs\Send`), false},
		{"count() is not a user method", "count($a)", method("count", `App\Models\Story`), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caller := ch("app/Http/C.php", tc.content, map[string]string{"name": "show", "namespace": `App\Http`})
			if got := Accepts(phpSrc, caller, tc.def); got != tc.want {
				t.Fatalf("Accepts = %v, want %v", got, tc.want)
			}
		})
	}
}
