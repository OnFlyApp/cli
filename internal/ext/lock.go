package ext

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Installed struct {
	Version    string   `json:"version"`
	File       string   `json:"file"`
	SHA256     string   `json:"sha256"`
	Source     string   `json:"source"`
	Extensions []string `json:"extensions"`
}

func lockPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".onfly", "plugins", "installed.json")
}

func LoadLock(projectRoot string) (map[string]Installed, error) {
	body, err := os.ReadFile(lockPath(projectRoot))
	if os.IsNotExist(err) {
		return map[string]Installed{}, nil
	}
	if err != nil {
		return nil, err
	}
	var lock map[string]Installed
	if err := json.Unmarshal(body, &lock); err != nil {
		return nil, fmt.Errorf("ext: installed.json: %w", err)
	}
	if lock == nil {
		lock = map[string]Installed{}
	}
	return lock, nil
}

func SaveLock(projectRoot string, lock map[string]Installed) error {
	if lock == nil {
		lock = map[string]Installed{}
	}
	if err := os.MkdirAll(filepath.Dir(lockPath(projectRoot)), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return writeAtomic(lockPath(projectRoot), body)
}

func writeAtomic(path string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.Write(body)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		os.Remove(name)
		if werr != nil {
			return werr
		}
		if serr != nil {
			return serr
		}
		return cerr
	}
	return os.Rename(name, path)
}

func HashFile(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return "", fmt.Errorf("ext: plugin is not a regular file")
	}
	if st.Size() > maxPluginBytes {
		return "", fmt.Errorf("ext: plugin exceeds 64MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxPluginBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxPluginBytes {
		return "", fmt.Errorf("ext: plugin exceeds 64MiB")
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func withPluginLock(root string, fn func() error) error {
	dir := filepath.Join(root, ".onfly", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "install.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := flockExclusive(f); err != nil {
		return err
	}
	defer flockUnlock(f)
	return fn()
}

func versionLess(a, b string) bool {
	as := strings.Split(strings.SplitN(a, "-", 2)[0], ".")
	bs := strings.Split(strings.SplitN(b, "-", 2)[0], ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		ai, aok := versionPart(as, i)
		bi, bok := versionPart(bs, i)
		if !aok || !bok {
			return a < b
		}
		if ai != bi {
			return ai < bi
		}
	}
	return false
}

func versionPart(parts []string, i int) (int, bool) {
	if i >= len(parts) {
		return 0, true
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0, false
	}
	return n, true
}
