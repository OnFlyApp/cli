package parser

import (
	"context"
	"strings"
	"testing"
)

func TestTextParserMarkdownSections(t *testing.T) {
	src := "intro line\n\n# Install\nrun it\n\n## Flags\n--x\n"
	got, err := TextParser{}.Parse(context.Background(), "/p/README.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	names := []string{got[0].Metadata["name"], got[1].Metadata["name"], got[2].Metadata["name"]}
	want := []string{"README.md", "README.md#Install", "README.md#Flags"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names %v", names)
		}
	}
	if got[1].Type != "section" || got[1].Metadata["start_line"] != "3" || got[1].Metadata["end_line"] != "5" {
		t.Fatalf("%+v", got[1])
	}
}

func TestTextParserBlocksForOtherFiles(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 70; i++ {
		b.WriteString("key: value\n")
		if i%10 == 9 {
			b.WriteString("\n")
		}
	}
	got, err := TextParser{}.Parse(context.Background(), "/p/conf.yaml", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0].Type != "block" || got[0].Metadata["name"] != "conf.yaml:1" {
		t.Fatalf("%+v", got)
	}
}

func TestTextParserEmptyFile(t *testing.T) {
	got, err := TextParser{}.Parse(context.Background(), "/p/a.md", []byte("  \n\n"))
	if err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestTextParserHeadingInsideFenceIsNotASection(t *testing.T) {
	src := "# Real\n```sh\n# comment\n```\n"
	got, _ := TextParser{}.Parse(context.Background(), "/p/a.md", []byte(src))
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestTextParserDuplicateHeadingsGetUniqueNames(t *testing.T) {
	src := "### Added\na\n### Added\nb\n### Added\nc\n"
	got, _ := TextParser{}.Parse(context.Background(), "/p/CHANGELOG.md", []byte(src))
	want := []string{"CHANGELOG.md#Added", "CHANGELOG.md#Added~2", "CHANGELOG.md#Added~3"}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i].Metadata["name"] != want[i] {
			t.Fatalf("names %q", got[i].Metadata["name"])
		}
	}
}

func TestTextParserFrontMatterIsNotHeadings(t *testing.T) {
	src := "---\ntitle: x\n# comment\n---\n# Real\nbody\n"
	got, _ := TextParser{}.Parse(context.Background(), "/p/a.md", []byte(src))
	if len(got) != 2 || got[0].Metadata["name"] != "a.md" || got[1].Metadata["name"] != "a.md#Real" {
		t.Fatalf("%+v", got)
	}
}

func TestTextParserFenceCloseMustMatchOpener(t *testing.T) {
	src := "# Real\n```sh\n~~~\n# shell comment\n```\nafter\n"
	got, _ := TextParser{}.Parse(context.Background(), "/p/a.md", []byte(src))
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}
