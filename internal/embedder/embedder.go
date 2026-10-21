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
	"syscall"
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
	return WithPrefixes(e, queryPrefix(cfg), cfg.DocumentPrefix), nil
}

// qwen3Instruct is the task line Qwen3-Embedding is trained to read ahead of a
// query; documents take none. Without it retrieval of a question drops.
const qwen3Instruct = "Instruct: Given a question about how a code base works, retrieve the source code that implements it\nQuery: "

// A profile is what a model needs that its name tells: the first whose match is
// part of the lower-cased model name gives the prefix of a query when
// embedder.query_prefix is not set.
type profile struct{ match, queryPrefix string }

var profiles = []profile{
	{match: "qwen3-embedding", queryPrefix: qwen3Instruct},
}

func queryPrefix(cfg config.Embedder) string {
	if cfg.QueryPrefix != "" {
		return cfg.QueryPrefix
	}
	for _, p := range profiles {
		if strings.Contains(strings.ToLower(cfg.Model), p.match) {
			return p.queryPrefix
		}
	}
	return ""
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

// DefaultTimeout is how long a request to the embedder may take when
// embedder.timeout_seconds is not set.
const DefaultTimeout = 120 * time.Second

func newHTTP() *http.Client { return newHTTPFor(0) }

func newHTTPFor(seconds int) *http.Client {
	timeout := DefaultTimeout
	if seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	return &http.Client{Timeout: timeout}
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
	if errors.Is(err, syscall.ECONNREFUSED) {
		return false
	}
	// A request that timed out is not sent again whole: the caller splits a
	// batch that is too big for the server, and a single text is retried there.
	var ne net.Error
	return errors.As(err, &ne) && !ne.Timeout()
}

// IsTimeout says that the server did not answer in time, which a smaller
// batch may cure.
func IsTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
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
