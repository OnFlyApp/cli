package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func outlineStore(t *testing.T) *tools {
	t.Helper()
	st := openTestStore(t)
	vec := []float32{1, 0, 0, 1}
	mk := func(file, name, recv, line string) vectorstore.EmbeddedChunk {
		meta := map[string]string{"name": name, "start_line": line, "end_line": line}
		if recv != "" {
			meta["receiver"] = recv
		}
		return vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: file, Type: "function", Content: "func " + name + "() {}", Metadata: meta}, Vector: vec}
	}
	if err := storetest.Save(context.Background(), st, []vectorstore.EmbeddedChunk{
		mk("pkg/user.go", "Save", "User", "30"),
		mk("pkg/user.go", "NewUser", "", "10"),
		mk("pkg/order.go", "Save", "Order", "5"),
		mk("pkg/order.go", "saveAll", "", "50"),
	}); err != nil {
		t.Fatal(err)
	}
	return &tools{store: st, root: t.TempDir()}
}

func TestFileOutlineIsInFileOrder(t *testing.T) {
	h := outlineStore(t)
	_, out, err := h.fileOutline(context.Background(), nil, outlineIn{FilePath: "./pkg/user.go"})
	if err != nil || len(out.Symbols) != 2 || out.Symbols[0].Name != "NewUser" || out.Symbols[1].Receiver != "User" {
		t.Fatalf("%+v %v", out, err)
	}
	for _, bad := range []string{"", "../x.go", "pkg/missing.go"} {
		if _, _, err := h.fileOutline(context.Background(), nil, outlineIn{FilePath: bad}); err == nil {
			t.Fatalf("%q must fail", bad)
		}
	}
}

func TestFindSymbolsPrefixAndGlob(t *testing.T) {
	h := outlineStore(t)
	ctx := context.Background()
	_, out, err := h.findSymbols(ctx, nil, findIn{Pattern: "save"})
	if err != nil || len(out.Symbols) != 3 {
		t.Fatalf("prefix: %+v %v", out, err)
	}
	if out.Symbols[0].Name != "Save" {
		t.Fatalf("exact name must come first: %+v", out.Symbols)
	}
	_, out, _ = h.findSymbols(ctx, nil, findIn{Pattern: "*user"})
	if len(out.Symbols) != 1 || out.Symbols[0].Name != "NewUser" || out.Symbols[0].FilePath != "pkg/user.go" {
		t.Fatalf("glob: %+v", out)
	}
	_, out, _ = h.findSymbols(ctx, nil, findIn{Pattern: "save", Limit: 1})
	if len(out.Symbols) != 1 || out.Omitted == 0 {
		t.Fatalf("limit: %+v", out)
	}
	if _, _, err := h.findSymbols(ctx, nil, findIn{Pattern: " "}); err == nil {
		t.Fatal("blank pattern must fail")
	}
}

func TestPromptsAreListedAndFilled(t *testing.T) {
	ctx := context.Background()
	session := openServer(t, config.Config{}, openTestStore(t), fixedEmbedder{v: []float32{1, 0, 0, 1}})
	listed, err := session.ListPrompts(ctx, &sdkmcp.ListPromptsParams{})
	if err != nil || len(listed.Prompts) != 2 {
		t.Fatalf("%+v %v", listed, err)
	}
	got, err := session.GetPrompt(ctx, &sdkmcp.GetPromptParams{Name: "review-impact", Arguments: map[string]string{"name": "Save"}})
	if err != nil || len(got.Messages) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	text := got.Messages[0].Content.(*sdkmcp.TextContent).Text
	if !strings.Contains(text, "`Save`") || strings.Contains(text, "{name}") {
		t.Fatalf("%s", text)
	}
	if _, err := session.GetPrompt(ctx, &sdkmcp.GetPromptParams{Name: "explain-component"}); err == nil {
		t.Fatal("a missing name must fail")
	}
}
