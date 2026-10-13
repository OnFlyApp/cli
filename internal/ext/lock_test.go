package ext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockRoundTripAndMissing(t *testing.T) {
	dir := t.TempDir()
	lock, err := LoadLock(dir)
	if err != nil || lock == nil || len(lock) != 0 {
		t.Fatalf("%v %+v", err, lock)
	}
	lock["php"] = Installed{
		Version: "1.2.0", File: "php-parser.wasm", SHA256: "abc",
		Source: "registry", Extensions: []string{".php"},
	}
	if err := SaveLock(dir, lock); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLock(dir)
	if err != nil || got["php"].Version != "1.2.0" || got["php"].File != "php-parser.wasm" {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".onfly", "plugins", "installed.json")); err != nil {
		t.Fatal(err)
	}
}

func TestVersionLess(t *testing.T) {
	if !versionLess("1.2.9", "1.2.10") || versionLess("1.2.0", "1.2.0") || versionLess("1.3.0", "1.2.9") {
		t.Fatal("numeric order")
	}
}
