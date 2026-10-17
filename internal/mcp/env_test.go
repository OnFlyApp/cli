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

func TestEnvToolReportsMissingAndUnused(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("APP_KEY=\nOLD_FLAG=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "<?php\nreturn ['k' => env('APP_KEY'), 'm' => env('MAIL_HOST')];\n"
	if err := os.WriteFile(filepath.Join(root, "config", "app.php"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	chunk := vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: "config/app.php", Type: "function", Content: "return ['k' => env('APP_KEY'), 'm' => env('MAIL_HOST')];", Metadata: map[string]string{"name": "cfg", "start_line": "2"}}, Vector: []float32{1, 0, 0, 1}}
	if err := saveHashed(store, root, []vectorstore.EmbeddedChunk{chunk}); err != nil {
		t.Fatal(err)
	}
	h := &tools{root: root, store: store, maxTokens: 8192}
	res, _, err := h.runEnv(context.Background(), nil, envIn{})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(res)
	for _, want := range []string{"<env_files>.env.example (2 keys)", "- APP_KEY  defined: .env.example  used: config/app.php:2", "<missing_from_env_files>\n- MAIL_HOST", "<unused>\n- OLD_FLAG  defined: .env.example"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") {
		t.Fatal("values are never reported")
	}
	filtered, _, _ := h.runEnv(context.Background(), nil, envIn{Filter: "mail"})
	if strings.Contains(resultText(filtered), "APP_KEY") || !strings.Contains(resultText(filtered), "MAIL_HOST") {
		t.Fatalf("%s", resultText(filtered))
	}
}
