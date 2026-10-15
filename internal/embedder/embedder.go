package embedder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/config"
)

type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

func New(cfg config.Embedder) (Embedder, error) {
	e, err := newProvider(cfg)
	if err != nil {
		return nil, err
	}
	return WithPrefixes(e, cfg.QueryPrefix, cfg.DocumentPrefix), nil
}

func newProvider(cfg config.Embedder) (Embedder, error) {
	switch cfg.Provider {
	case "ollama":
		return newOllama(cfg)
	case "openai":
		return newOpenAI(cfg)
	case "voyage":
		return newVoyage(cfg)
	default:
		return nil, fmt.Errorf("embedder provider %q is not available", cfg.Provider)
	}
}

func newHTTP() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

type statusError struct {
	Status        int
	Body          string
	RetryAfter    time.Duration
	HasRetryAfter bool
}

func (e *statusError) Error() string {
	return fmt.Sprintf("embedder: %d: %s", e.Status, e.Body)
}

func postJSON(ctx context.Context, client *http.Client, url, key string, body []byte) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt - 1)
			var se *statusError
			if errors.As(last, &se) && se.HasRetryAfter && se.RetryAfter <= 30*time.Second {
				delay = se.RetryAfter
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		buf, err := postOnce(ctx, client, url, key, body)
		if err == nil {
			return buf, nil
		}
		last = err
		if ctx.Err() != nil || !retryable(err) {
			return nil, err
		}
	}
	return nil, last
}

// retryable covers rate limits, gateway errors and network errors. A plain 500
// is not retried: Ollama returns it for permanent errors such as a missing model.
func retryable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusTooManyRequests, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	var ne net.Error
	return errors.As(err, &ne)
}

func backoff(retry int) time.Duration {
	const maxDelay = 10 * time.Second
	base := 200 * time.Millisecond
	shift := retry
	if shift > 16 {
		shift = 16
	}
	d := base << shift
	if d <= 0 || d > maxDelay {
		d = maxDelay
	}
	d = time.Duration(float64(d) * (0.5 + rand.Float64()))
	if d > maxDelay {
		d = maxDelay
	}
	return d
}

func postOnce(ctx context.Context, client *http.Client, url, key string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	const maxBody = 32 << 20
	buf, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > maxBody {
		return nil, fmt.Errorf("embedder: response exceeds 32MiB")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(buf))
		if len(msg) > 4096 {
			msg = msg[:4096]
		}
		se := &statusError{Status: res.StatusCode, Body: msg}
		if v := res.Header.Get("Retry-After"); v != "" {
			if sec, err := strconv.Atoi(v); err == nil && sec >= 0 {
				se.RetryAfter = time.Duration(sec) * time.Second
				se.HasRetryAfter = true
			}
		}
		return nil, se
	}
	return buf, nil
}

func checkDims(vectors [][]float32, texts int, dimensions int) error {
	if len(vectors) != texts {
		return fmt.Errorf("embedder: got %d vectors, want %d", len(vectors), texts)
	}
	for _, v := range vectors {
		if len(v) != dimensions {
			return fmt.Errorf("embedder: dimensions got %d, want %d", len(v), dimensions)
		}
	}
	return nil
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}
