package ext

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const DefaultRegistryURL = "https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json"

const maxRedirects = 5

var maxPluginBytes int64 = 64 << 20

type Release struct {
	URL        string   `json:"url"`
	SHA256     string   `json:"sha256"`
	Extensions []string `json:"extensions"`
}

type Plugin struct {
	Latest   string             `json:"latest"`
	Versions map[string]Release `json:"versions"`
}

type Manifest map[string]Plugin

func RegistryURL() string {
	if u := strings.TrimSpace(os.Getenv("ONFLY_REGISTRY_URL")); u != "" {
		return u
	}
	return DefaultRegistryURL
}

func NewClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second, CheckRedirect: rejectPlainRedirect}
}

func rejectPlainRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("ext: redirect to %s is not https", req.URL.Redacted())
	}
	if len(via) >= maxRedirects {
		return fmt.Errorf("ext: too many redirects")
	}
	return nil
}

func requireHTTPS(raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("ext: download url is not https")
	}
	return nil
}

func FetchManifest(ctx context.Context, client *http.Client) (Manifest, error) {
	if client == nil {
		client = NewClient()
	}
	body, err := getBytes(ctx, client, RegistryURL())
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	if m == nil {
		m = Manifest{}
	}
	return m, nil
}

func Resolve(m Manifest, name, version string) (string, Release, error) {
	p, ok := m[name]
	if !ok {
		return "", Release{}, fmt.Errorf("registry: plugin %q is not in the registry", name)
	}
	if version == "" || version == "latest" {
		version = p.Latest
	}
	rel, ok := p.Versions[version]
	if !ok {
		return "", Release{}, fmt.Errorf("registry: plugin %q has no version %q", name, version)
	}
	if strings.TrimSpace(rel.URL) == "" {
		return "", Release{}, fmt.Errorf("registry: plugin %q version %q has no url", name, version)
	}
	sum := strings.ToLower(strings.TrimSpace(rel.SHA256))
	if err := checkSHA256(sum); err != nil {
		return "", Release{}, fmt.Errorf("registry: plugin %q version %q checksum is invalid", name, version)
	}
	rel.SHA256 = sum
	if len(rel.Extensions) == 0 {
		return "", Release{}, fmt.Errorf("registry: plugin %q version %q has no extensions", name, version)
	}
	return version, rel, nil
}

func checkSHA256(sum string) error {
	raw, err := hex.DecodeString(sum)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("invalid sha256")
	}
	return nil
}

func getBytes(ctx context.Context, client *http.Client, raw string) ([]byte, error) {
	if err := requireHTTPS(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ext: download %s: %w", raw, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ext: download %s: %s", raw, res.Status)
	}
	buf, err := io.ReadAll(io.LimitReader(res.Body, maxPluginBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ext: download %s: %w", raw, err)
	}
	if int64(len(buf)) > maxPluginBytes {
		return nil, fmt.Errorf("ext: plugin exceeds 64MiB")
	}
	return buf, nil
}
