package ext

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var ErrChecksumMismatch = errors.New("ext: checksum mismatch")

func Download(ctx context.Context, client *http.Client, rawURL, wantSHA string) (string, string, error) {
	return download(ctx, client, rawURL, wantSHA, maxPluginBytes)
}

func download(ctx context.Context, client *http.Client, rawURL, wantSHA string, limit int64) (string, string, error) {
	if client == nil {
		client = NewClient()
	}
	if err := requireHTTPS(rawURL); err != nil {
		return "", "", err
	}
	f, err := os.CreateTemp("", "onfly_download_*")
	if err != nil {
		return "", "", err
	}
	path := f.Name()
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(path)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("ext: download %s: %w", rawURL, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("ext: download %s: %s", rawURL, res.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, limit+1))
	if err != nil {
		return "", "", fmt.Errorf("ext: download %s: %w", rawURL, err)
	}
	if n > limit {
		return "", "", fmt.Errorf("ext: plugin exceeds 64MiB")
	}
	if err := f.Close(); err != nil {
		return "", "", err
	}
	got := fmt.Sprintf("%x", h.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(wantSHA)) {
		return "", "", ErrChecksumMismatch
	}
	keep = true
	return path, got, nil
}

func MoveInto(tempPath, dest string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	staged, err := os.CreateTemp(dir, ".plugin-*")
	if err != nil {
		return err
	}
	stagedName := staged.Name()
	in, err := os.Open(tempPath)
	if err != nil {
		staged.Close()
		os.Remove(stagedName)
		return err
	}
	_, cerr := io.Copy(staged, in)
	in.Close()
	syncErr := staged.Sync()
	closeErr := staged.Close()
	if cerr != nil || syncErr != nil || closeErr != nil {
		os.Remove(stagedName)
		if cerr != nil {
			return cerr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	os.Remove(tempPath)
	info, err := os.Lstat(dest)
	if err != nil && !os.IsNotExist(err) {
		os.Remove(stagedName)
		return err
	}
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		os.Remove(stagedName)
		return fmt.Errorf("ext: %s is not a regular file", dest)
	}
	bak := dest + ".bak"
	if err == nil {
		os.Remove(bak)
		if err := os.Rename(dest, bak); err != nil {
			os.Remove(stagedName)
			return err
		}
	}
	if err := os.Rename(stagedName, dest); err != nil {
		RestoreBackup(dest)
		return err
	}
	return nil
}

func RestoreBackup(dest string) error {
	bak := dest + ".bak"
	if _, err := os.Lstat(bak); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	os.Remove(dest)
	return os.Rename(bak, dest)
}

func DiscardBackup(dest string) error {
	err := os.Remove(dest + ".bak")
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
