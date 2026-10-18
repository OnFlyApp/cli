package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

// symbolFixture has two functions called Open in different packages, a method
// Saved, and one caller of each.
func symbolFixture(t *testing.T) *tools {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"internal/telemetry/telemetry.go": "package telemetry\n\nfunc Open() {}\n\ntype Summary struct{}\n\nfunc (s Summary) Saved() int { return 0 }\n",
		"internal/index/index.go":         "package index\n\nfunc Open() {}\n",
		"cmd/stats.go":                    "package cmd\n\nimport \"x/internal/telemetry\"\n\nfunc printTelemetry() { telemetry.Open(); var s telemetry.Summary; s.Saved() }\n",
		"cmd/index.go":                    "package cmd\n\nimport \"x/internal/index\"\n\nfunc runIndex() { index.Open() }\n",
	}
	for rel, src := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meta := func(name, recv string, line string) map[string]string {
		m := map[string]string{"name": name, "start_line": line, "end_line": line}
		if recv != "" {
			m["receiver"] = recv
		}
		return m
	}
	v := []float32{1, 0, 0, 1}
	chunks := []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "internal/telemetry/telemetry.go", Type: "function", Content: "func Open() {}", Metadata: meta("Open", "", "3")}, Vector: v},
		{Chunk: parser.Chunk{FilePath: "internal/telemetry/telemetry.go", Type: "method", Content: "func (s Summary) Saved() int { return 0 }", Metadata: meta("Saved", "Summary", "7")}, Vector: v},
		{Chunk: parser.Chunk{FilePath: "internal/index/index.go", Type: "function", Content: "func Open() {}", Metadata: meta("Open", "", "3")}, Vector: v},
		{Chunk: parser.Chunk{FilePath: "cmd/stats.go", Type: "function", Content: "func printTelemetry() { telemetry.Open(); var s telemetry.Summary; s.Saved() }", Metadata: meta("printTelemetry", "", "5"), Outbound: []string{"Open", "Saved"}}, Vector: v},
		{Chunk: parser.Chunk{FilePath: "cmd/index.go", Type: "function", Content: "func runIndex() { index.Open() }", Metadata: meta("runIndex", "", "5"), Outbound: []string{"Open"}}, Vector: v},
		{Chunk: parser.Chunk{FilePath: "CHANGELOG.md", Type: "section", Content: "## v1 mentions Open", Metadata: meta("v1", "", "1"), Outbound: []string{"Open"}}, Vector: v},
	}
	store := openTestStore(t)
	if err := saveHashed(store, root, chunks); err != nil {
		t.Fatal(err)
	}
	return &tools{root: root, store: store, maxTokens: 8192}
}

func xrayText(t *testing.T, h *tools, target string) string {
	t.Helper()
	res, _, err := h.runXray(context.Background(), nil, xrayIn{Target: target})
	if err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return resultText(res)
}

func TestXrayQualifiedSymbolResolvesToItsPackage(t *testing.T) {
	h := symbolFixture(t)
	for _, target := range []string{"telemetry.Open", "internal/telemetry/telemetry.go:Open", "Summary.Saved", "telemetry.Summary.Saved"} {
		out := xrayText(t, h, target)
		if !strings.Contains(out, `<skeleton>`) || !strings.Contains(out, "package telemetry") {
			t.Fatalf("%s should open the telemetry file:\n%s", target, out)
		}
		if strings.Contains(out, "package index") {
			t.Fatalf("%s must not mix in the other Open:\n%s", target, out)
		}
		if !strings.Contains(out, "func printTelemetry") || strings.Contains(out, "func runIndex") {
			t.Fatalf("%s should list exactly its callers:\n%s", target, out)
		}
	}
}

func TestXrayAmbiguousNameShowsEveryDefinitionWithItsCallers(t *testing.T) {
	h := symbolFixture(t)
	out := xrayText(t, h, "Open")
	for _, want := range []string{`matches="2"`, `<definition at="internal/telemetry/telemetry.go:3">`, `<definition at="internal/index/index.go:3">`, "func printTelemetry", "func runIndex"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "<skeleton>") {
		t.Fatalf("an ambiguous name is not expanded into one file:\n%s", out)
	}
	if strings.Contains(out, "CHANGELOG.md") {
		t.Fatalf("a documentation page is not a caller:\n%s", out)
	}
	// Each definition lists only its own caller.
	// Definitions are ordered by path: index.go, then telemetry.go.
	idx := strings.Index(out, `<definition at="internal/index/index.go:3">`)
	tel := strings.Index(out, `<definition at="internal/telemetry/telemetry.go:3">`)
	if idx < 0 || tel < idx {
		t.Fatalf("unexpected order:\n%s", out)
	}
	first, second := out[idx:tel], out[tel:]
	if !strings.Contains(first, "runIndex") || strings.Contains(first, "printTelemetry") ||
		!strings.Contains(second, "printTelemetry") || strings.Contains(second, "runIndex") {
		t.Fatalf("callers are attributed to the wrong Open:\n%s", out)
	}
}

func TestXrayUnmatchedQualifierFallsBackToAllDefinitions(t *testing.T) {
	out := xrayText(t, symbolFixture(t), "nosuch.Open")
	if !strings.Contains(out, "<note>no definition of Open matches") || !strings.Contains(out, `matches="2"`) {
		t.Fatalf("%s", out)
	}
	if _, _, err := symbolFixture(t).runXray(context.Background(), nil, xrayIn{Target: "Nothing.Here"}); err == nil {
		t.Fatal("an unknown symbol is an error")
	}
}

// bigFixture is pkg/big.go with two functions of 16 lines each: First on lines
// 3-18 and Second on lines 20-35, both long enough to be collapsed.
func bigFixture(t *testing.T) *tools {
	t.Helper()
	root := t.TempDir()
	body := func(name, v string) string {
		s := "func " + name + "() int {\n"
		for i := 0; i < 13; i++ {
			s += "\t" + v + " := " + v + " + 1\n"
		}
		return s + "\treturn " + v + "\n}\n"
	}
	src := "package big\n\n" + body("First", "a") + "\n" + body("Second", "b")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "big.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(src, "\n")
	chunk := func(name string, start, end int) vectorstore.EmbeddedChunk {
		return vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: "pkg/big.go", Type: "function", Content: strings.Join(lines[start-1:end], "\n"),
			Metadata: map[string]string{"name": name, "start_line": itoa(start), "end_line": itoa(end)}}, Vector: []float32{1, 0, 0, 1}}
	}
	store := openTestStore(t)
	if err := saveHashed(store, root, []vectorstore.EmbeddedChunk{chunk("First", 3, 18), chunk("Second", 20, 35)}); err != nil {
		t.Fatal(err)
	}
	return &tools{root: root, store: store, maxTokens: 8192}
}

func TestXrayExpandsTheNamedSymbolInOneCall(t *testing.T) {
	defer func(v int) { smallFile = v }(smallFile)
	smallFile = 0
	h := bigFixture(t)
	for _, target := range []string{"big.go:Second", "pkg/big.go:Second", "Second"} {
		out := xrayText(t, h, target)
		if !strings.Contains(out, `target="pkg/big.go"`) {
			t.Fatalf("%s should open the file:\n%s", target, out)
		}
		if !strings.Contains(out, "20| func Second() int {") || !strings.Contains(out, "21| \tb := b + 1") || !strings.Contains(out, "34| \treturn b") {
			t.Fatalf("%s: Second must be expanded with its own line numbers:\n%s", target, out)
		}
		if strings.Contains(out, "a := a + 1") || !strings.Contains(out, `original lines 4-17`) || !strings.Contains(out, `focus_symbol="First" expands it`) {
			t.Fatalf("%s: First stays collapsed and says how to expand it:\n%s", target, out)
		}
	}
	// The other way round.
	out := xrayText(t, h, "big.go:First")
	if !strings.Contains(out, "4| \ta := a + 1") || strings.Contains(out, "b := b + 1") {
		t.Fatalf("%s", out)
	}
}

func TestXrayWithAnUnknownSymbolInARealFileReturnsTheFileAndWhatItHas(t *testing.T) {
	out := xrayText(t, bigFixture(t), "big.go:Nope")
	if !strings.Contains(out, `<note>no symbol "Nope" in this file`) || !strings.Contains(out, "First, Second") || !strings.Contains(out, "<skeleton>") {
		t.Fatalf("%s", out)
	}
	if _, _, err := bigFixture(t).runXray(context.Background(), nil, xrayIn{Target: "nothere.go:Nope"}); err == nil {
		t.Fatal("a file that does not exist is still an error")
	}
}

func TestXrayAmbiguousNameCarriesTheCodeOfEachDefinition(t *testing.T) {
	out := xrayText(t, symbolFixture(t), "Open")
	if strings.Count(out, "<code>\nfunc Open() {}\n</code>") != 2 {
		t.Fatalf("with two definitions each comes with its code:\n%s", out)
	}
}
