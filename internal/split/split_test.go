package split

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/axitdev/onfly/internal/parser"
)

func TestSplitNamesAndOverlap(t *testing.T) {
	in := []parser.Chunk{{
		FilePath: "a.go",
		Type:     "class",
		Content:  "aaaaaaaaaa\n\nbbbbbbbbbb\n\ncccccccccc",
		Metadata: map[string]string{"name": "UserService"},
		Outbound: []string{"Other"},
	}}
	got := Split(in, 10, 2, func(s string) int { return len(s) })
	if len(got) < 2 {
		t.Fatalf("parts = %d", len(got))
	}
	if got[0].Metadata["name"] != "UserService" || got[0].Metadata["part"] != "1/"+strconv.Itoa(len(got)) {
		t.Fatalf("name %q part %q", got[0].Metadata["name"], got[0].Metadata["part"])
	}
	if len(got[0].Outbound) != 1 || got[0].Outbound[0] != "Other" {
		t.Fatalf("outbound = %+v", got[0].Outbound)
	}
	if len(got[1].Outbound) != 0 || got[1].FilePath != "a.go" || got[1].Metadata["name"] != "UserService" {
		t.Fatalf("child = %+v", got[1])
	}
	if !strings.Contains(got[1].Content, got[0].Content[len(got[0].Content)-2:]) {
		t.Fatalf("no overlap: %q / %q", got[1].Content, got[0].Content)
	}
}

func TestSplitKeepsSpaces(t *testing.T) {
	in := []parser.Chunk{{Content: "hello world extra", Metadata: map[string]string{"name": "N"}}}
	got := Split(in, 12, 0, func(s string) int { return len(s) })
	if len(got) < 2 {
		t.Fatalf("parts = %+v", got)
	}
	joined := strings.Join(nil, "")
	for _, part := range got {
		joined += part.Content
	}
	if !strings.Contains(joined, "hello world") {
		t.Fatalf("lost space: %#v", got)
	}
}

func TestSplitOverlapStaysOnRuneBoundary(t *testing.T) {
	body := strings.Repeat("a", 8) + "世界" + "zz"
	got := Split([]parser.Chunk{{Content: body, Metadata: map[string]string{"name": "N"}}}, 10, 4, func(s string) int { return len(s) })
	if len(got) < 2 {
		t.Fatalf("%+v", got)
	}
	for _, part := range got {
		if !utf8.ValidString(part.Content) {
			t.Fatalf("invalid utf-8 %q", part.Content)
		}
	}
}

func TestSplitKeepsSmallChunk(t *testing.T) {
	in := []parser.Chunk{{Content: "short", Metadata: map[string]string{"name": "A"}}}
	got := Split(in, 10, 2, func(s string) int { return len(s) })
	if len(got) != 1 || got[0].Metadata["name"] != "A" {
		t.Fatalf("%+v", got)
	}
}

func BenchmarkSplitLongLine(b *testing.B) {
	text := strings.Repeat("abcdefghij", 40000) // 400 KB, no separators
	count := func(s string) int { return (len(s) + 3) / 4 }
	for i := 0; i < b.N; i++ {
		Split([]parser.Chunk{{Content: text}}, 1000, 100, count)
	}
}
