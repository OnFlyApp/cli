package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBlacklistAndGitignore(t *testing.T) {
	dir := t.TempDir()
	body := "secrets/\n!secrets/keep.go\n*.log\n!keep.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "web")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".gitignore"), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".env", ".ENV", ".env.local", ".env/local.go", "keys/id_rsa", "keys/ID_RSA", "keys.pem/a.go", "a.pem", "A.PEM", "vendor/x.go", "Vendor/x.go", "node_modules/a.js", ".git/config", ".Onfly/config.yaml", "secrets/a.go", "secrets/keep.go", "keep.log", "web/dist/app.js", "debug.log"} {
		if !set.Ignored(rel, false) {
			t.Fatalf("expected ignore %s", rel)
		}
	}
	if set.Ignored("src/main.go", false) {
		t.Fatal("src/main.go ignored")
	}
}
