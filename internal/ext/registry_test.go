package ext

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultRegistryURL(t *testing.T) {
	const want = "https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json"
	if DefaultRegistryURL != want {
		t.Fatal(DefaultRegistryURL)
	}
}

func TestRegistryResolveAndOverride(t *testing.T) {
	wasm := []byte("wasm-bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(wasm))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry.json" {
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{
			"php": {
				"latest": "1.2.0",
				"versions": {
					"1.2.0": {"url": "https://%s/php.wasm", "sha256": %q, "extensions": [".php", ".inc", ".phtml"]},
					"1.0.0": {"url": "https://%s/old.wasm", "sha256": %q, "extensions": [".php"]}
				}
			}
		}`, r.Host, sum, r.Host, sum)
	}))
	defer srv.Close()
	t.Setenv("ONFLY_REGISTRY_URL", srv.URL+"/registry.json")
	if RegistryURL() != srv.URL+"/registry.json" {
		t.Fatal(RegistryURL())
	}
	client := NewClient()
	client.Transport = srv.Client().Transport
	m, err := FetchManifest(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	ver, rel, err := Resolve(m, "php", "")
	if err != nil || ver != "1.2.0" || rel.SHA256 != sum || len(rel.Extensions) != 3 {
		t.Fatalf("%s %+v %v", ver, rel, err)
	}
	ver, _, err = Resolve(m, "php", "latest")
	if err != nil || ver != "1.2.0" {
		t.Fatalf("latest: %s %v", ver, err)
	}
	if _, _, err = Resolve(m, "php", "9.9.9"); err == nil || !strings.Contains(err.Error(), `plugin "php" has no version "9.9.9"`) {
		t.Fatal(err)
	}
	if _, _, err = Resolve(m, "nope", ""); err == nil || !strings.Contains(err.Error(), `plugin "nope" is not in the registry`) {
		t.Fatal(err)
	}
}

func TestRegistryRejectsHTTP(t *testing.T) {
	t.Setenv("ONFLY_REGISTRY_URL", "http://example.com/registry.json")
	_, err := FetchManifest(context.Background(), NewClient())
	if err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatal(err)
	}
}
