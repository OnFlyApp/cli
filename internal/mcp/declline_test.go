package mcp

import (
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func declChunk(file, typ, start, content string) parser.Chunk {
	return parser.Chunk{FilePath: file, Type: typ, Content: content, Metadata: map[string]string{"start_line": start}}
}

func TestDeclLineSkipsDocumentationAndAttributes(t *testing.T) {
	phpMethod := "    /**\n     * Run the route through its middleware.\n     *\n     * @return mixed\n     */\n    protected function runRouteWithinStack(Route $route, Request $request)\n    {\n        return 1;\n    }"
	for _, tc := range []struct {
		name  string
		chunk parser.Chunk
		want  int
	}{
		{"a PHP method under a docblock", declChunk("Router.php", "method", "818", phpMethod), 823},
		{"a PHP method with an attribute under the docblock", declChunk("C.php", "method", "10", "    /** doc */\n    #[Override]\n    public function save()\n    {\n    }"), 12},
		{"a one-line docblock", declChunk("C.php", "method", "10", "/** @return int */\npublic function id()"), 11},
		{"a Go function under a doc comment", declChunk("stats.go", "function", "94", "// printTelemetry shows what\n// the tools kept out.\nfunc printTelemetry() error {\n\treturn nil\n}"), 96},
		{"a Java method under an annotation", declChunk("A.java", "method", "5", "@Override\npublic String name() {"), 6},
		{"a Python comment above a function", declChunk("a.py", "function", "3", "# helper\ndef run():\n    pass"), 4},
		{"no documentation: start_line stays", declChunk("a.go", "function", "7", "func f() {}"), 7},
		{"a Markdown section starts at its heading", declChunk("README.md", "section", "20", "# Install\ntext"), 20},
		{"a # line in a Go chunk is code, not a comment", declChunk("a.go", "function", "9", "#define X\nfunc f() {}"), 9},
		{"only comments: start_line stays", declChunk("a.php", "method", "4", "/** nothing */\n"), 4},
	} {
		if got := declLine(tc.chunk); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestLocationPointsAtTheSignature(t *testing.T) {
	c := declChunk("src/Illuminate/Routing/Router.php", "method", "816", "    /**\n     * Run the given route within a Stack \"onion\" instance.\n     *\n     * @param  \\Illuminate\\Routing\\Route  $route\n     * @return mixed\n     */\n    protected function runRouteWithinStack(Route $route, Request $request)\n    {\n    }")
	if got := location(c); got != "src/Illuminate/Routing/Router.php:822" {
		t.Fatalf("%s", got)
	}
}
