package split

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/axitdev/onfly/internal/parser"
)

func FuzzSplit(f *testing.F) {
	for _, s := range []string{"", "a b c", "func x() {\n\treturn\n}\n\n", strings.Repeat("é", 50), strings.Repeat("x", 300)} {
		f.Add(s)
	}
	count := func(s string) int { return (len(s) + 3) / 4 }
	f.Fuzz(func(t *testing.T, text string) {
		if !utf8.ValidString(text) {
			return
		}
		for _, c := range Split([]parser.Chunk{{Content: text}}, 20, 4, count) {
			if count(c.Content) > 20 && utf8.RuneCountInString(c.Content) > 1 {
				t.Fatalf("part over limit: %d tokens", count(c.Content))
			}
			if !utf8.ValidString(c.Content) {
				t.Fatalf("invalid utf8 part %q", c.Content)
			}
		}
	})
}
