package query

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

func TestIDFWeightFallsWithFrequency(t *testing.T) {
	if w := idfWeight(1, 1000); w != 1 {
		t.Fatalf("a word in one chunk weighs 1: %v", w)
	}
	if w := idfWeight(0, 1000); w != 1 {
		t.Fatalf("a word in no chunk weighs 1: %v", w)
	}
	if w := idfWeight(1000, 1000); w > 0.01 {
		t.Fatalf("a word in every chunk weighs nothing: %v", w)
	}
	if w := idfWeight(100, 1000); w >= minTermWeight {
		t.Fatalf("a word in 10%% of chunks is too common to name a module: %v", w)
	}
	if w := idfWeight(10, 1000); w < minTermWeight || w > 1 {
		t.Fatalf("a word in 1%% of chunks names a module: %v", w)
	}
	if idfWeight(5, 0) != 1 {
		t.Fatal("without statistics every word weighs 1")
	}
}

// siteCorpus is a small site generator: "content" and "page" are everywhere, the
// docs are a pile of Markdown that repeats the question, and the answer is toc.go.
func siteCorpus(t *testing.T) *Searcher {
	t.Helper()
	st, err := vectorstore.OpenSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	v := []float32{1, 0, 0, 1}
	var chunks []vectorstore.EmbeddedChunk
	add := func(file, typ, name, content string) {
		chunks = append(chunks, vectorstore.EmbeddedChunk{
			Chunk:  parser.Chunk{FilePath: file, Type: typ, Content: content, Metadata: map[string]string{"name": name}},
			Vector: v,
		})
	}
	for i := 0; i < 60; i++ {
		add(fmt.Sprintf("hugolib/page%02d.go", i), "function", fmt.Sprintf("renderPage%02d", i), "func renderPage() { content := page.Content() }")
	}
	for i := 0; i < 40; i++ {
		add(fmt.Sprintf("docs/content/en/topic%02d.md", i), "section", fmt.Sprintf("topic%02d.md", i), "# Table of contents\nThe table of contents lists headings of the content page.")
	}
	add("create/content.go", "function", "NewContent", "func NewContent() { // new content for the table }")
	add("markup/goldmark/toc.go", "function", "TableOfContents", "func TableOfContents() { // build the table of contents }")
	add("markup/goldmark/toc.go", "function", "extractHeadings", "func extractHeadings() { // collect the headings }")
	if err := storetest.Save(ctx, st, chunks); err != nil {
		t.Fatal(err)
	}
	return &Searcher{Store: st, Emb: vecEmb{v: v}}
}

func TestTermWeightsMakeCommonWordsLight(t *testing.T) {
	s := siteCorpus(t)
	w := s.termWeights(vectorstore.WithoutProse(context.Background()), []string{"content", "headings"}, "")
	if w["content"] >= minTermWeight || w["headings"] <= 0.6 || w["headings"] <= w["content"] {
		t.Fatalf("%v", w)
	}
}

func TestFocusKeepsDocumentationOutOfTheCandidates(t *testing.T) {
	s := siteCorpus(t)
	ctx := context.Background()
	got, err := s.RunFocus(ctx, Request{Query: "table of contents headings", Limit: 5})
	if err != nil || len(got) == 0 {
		t.Fatalf("%v %v", got, err)
	}
	for _, c := range got {
		if vectorstore.IsProse(c.FilePath) {
			t.Fatalf("prose took a place of code: %s", c.FilePath)
		}
	}
	if got[0].FilePath != "markup/goldmark/toc.go" {
		t.Fatalf("the file that covers the question leads: %v", got[0].FilePath)
	}
	docs, err := s.RunFocus(ctx, Request{Query: "documentation table of contents headings", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	prose := 0
	for _, c := range docs {
		if vectorstore.IsProse(c.FilePath) {
			prose++
		}
	}
	if prose == 0 {
		t.Fatalf("a query about documentation searches it: %v", docs)
	}
}

func TestCohesionLiftsTheFileThatCoversTheQuestion(t *testing.T) {
	s := siteCorpus(t)
	ctx := context.Background()
	terms := queryWords("table of contents headings")
	weights := s.termWeights(vectorstore.WithoutProse(ctx), terms, "")
	decoy := parser.Chunk{FilePath: "create/content.go", Type: "function", Content: "func NewContent()", Metadata: map[string]string{"name": "NewContent"}}
	hit := parser.Chunk{FilePath: "markup/goldmark/toc.go", Type: "function", Content: "func extractHeadings()", Metadata: map[string]string{"name": "extractHeadings"}}
	got := s.boostCohesion(ctx, []parser.Chunk{decoy, hit}, terms, weights)
	if got[0].FilePath != "markup/goldmark/toc.go" {
		t.Fatalf("%v", got)
	}
	same := s.boostCohesion(ctx, []parser.Chunk{decoy}, terms, weights)
	if len(same) != 1 {
		t.Fatalf("%v", same)
	}
}

func TestPathBoostIgnoresCommonWords(t *testing.T) {
	ranked := func() []parser.Chunk {
		var out []parser.Chunk
		for _, f := range []string{"a.go", "b.go", "c.go", "hugolib/page.go"} {
			out = append(out, parser.Chunk{FilePath: f, Metadata: map[string]string{"name": f}})
		}
		return out
	}
	order := func(cs []parser.Chunk) string {
		var b strings.Builder
		for _, c := range cs {
			b.WriteString(c.FilePath + " ")
		}
		return b.String()
	}
	common := boostPathTokens(ranked(), nil, []string{"page"}, map[string]float64{"page": 0.05})
	if order(common) != "a.go b.go c.go hugolib/page.go " {
		t.Fatalf("a word in many chunks names no module: %s", order(common))
	}
	rare := boostPathTokens(ranked(), nil, []string{"page"}, map[string]float64{"page": 1})
	if order(rare) != "a.go b.go hugolib/page.go c.go " {
		t.Fatalf("a rare word moves the file it names: %s", order(rare))
	}
}
