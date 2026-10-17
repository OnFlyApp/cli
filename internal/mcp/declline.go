package mcp

import (
	"path"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
)

// declTypes are the chunks that begin with their documentation: a parser puts a
// docblock, doc comment or attribute in the chunk so that it is searched and
// embedded with the code, and start_line is where that text begins. For these
// the line a reader wants is the declaration itself.
var declTypes = map[string]bool{
	"function": true, "method": true, "closure": true, "class": true, "interface": true,
	"trait": true, "struct": true, "type": true, "enum": true, "constant": true, "property": true,
}

// declLine is the line of the declaration in a chunk, past any leading
// comments, docblocks, attributes and annotations. It is start_line when the
// chunk has none, or is not a declaration.
func declLine(c parser.Chunk) int {
	start := lineNum(c.Metadata["start_line"])
	if start <= 0 || !declTypes[strings.ToLower(c.Type)] {
		return start
	}
	ext := strings.ToLower(path.Ext(c.FilePath))
	inBlock := false
	for i, line := range strings.Split(c.Content, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case inBlock:
			inBlock = !strings.Contains(t, "*/")
		case t == "":
		case strings.HasPrefix(t, "/*"):
			inBlock = !strings.Contains(t, "*/")
		case strings.HasPrefix(t, "//"), strings.HasPrefix(t, "#["), strings.HasPrefix(t, "@"):
		case strings.HasPrefix(t, "#") && (ext == ".py" || ext == ".rb" || ext == ".sh"):
		default:
			return start + i
		}
	}
	return start
}
