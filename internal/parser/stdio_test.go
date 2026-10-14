package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStdioParse(t *testing.T) {
	script := writeScript(t, `#!/bin/sh
cat >/dev/null
printf '%s\n' '[{"file_path":"","type":"class","content":"class A {}","metadata":{"name":"User"}}]'
`)
	p := newStdioParser(script)
	got, err := p.Parse(context.Background(), "app/User.php", []byte("<?php"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Metadata["name"] != "User" || got[0].FilePath != "app/User.php" {
		t.Fatalf("%+v", got)
	}
}

func TestStdioExitError(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\necho boom >&2\nexit 1\n")
	p := newStdioParser(script)
	_, err := p.Parse(context.Background(), "a.php", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestStdioTimeout(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\nsleep 30\n")
	p := newStdioParser(script)
	start := time.Now()
	if _, err := p.Parse(context.Background(), "a.php", []byte("x")); err == nil || time.Since(start) > 8*time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
	ok := writeScript(t, "#!/bin/sh\nprintf '[]'\n")
	p2 := newStdioParser(ok)
	got, err := p2.Parse(context.Background(), "a.php", nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestStdioBadJSON(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\necho not-json\n")
	p := newStdioParser(script)
	_, err := p.Parse(context.Background(), "a.php", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "json") {
		t.Fatalf("err = %v", err)
	}
}
