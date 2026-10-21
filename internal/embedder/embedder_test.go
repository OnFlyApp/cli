package embedder

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/config"
)

func TestOllamaEmbed(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		})
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{Provider: "ollama", Endpoint: srv.URL, Model: "nomic-embed-text", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := emb.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/embed" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotBody["model"] != "nomic-embed-text" {
		t.Fatalf("body = %#v", gotBody)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][1] != 0.4 {
		t.Fatalf("%v", vecs)
	}
}

func TestOllamaErrorAndDimensions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing model", http.StatusInternalServerError)
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{Provider: "ollama", Endpoint: srv.URL, Model: "m", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = emb.Embed(context.Background(), "a")
	if err == nil || !contains(err.Error(), "missing model") {
		t.Fatalf("err = %v", err)
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0.1}}})
	}))
	defer ok.Close()
	emb, err = New(config.Embedder{Provider: "ollama", Endpoint: ok.URL, Model: "m", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = emb.Embed(context.Background(), "a")
	if err == nil || !contains(err.Error(), "dimensions") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIOrderAndFactory(t *testing.T) {
	var auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{0, 1}},
				{"index": 0, "embedding": []float32{1, 0}},
			},
		})
	}))
	defer srv.Close()
	ask := true
	emb, err := New(config.Embedder{
		Provider: "openai", Endpoint: srv.URL, Model: "text-embedding-3-small",
		Dimensions: 2, APIKey: "test-key", SendDimensions: &ask,
	})
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := emb.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer test-key" {
		t.Fatalf("auth = %s", auth)
	}
	if body["model"] != "text-embedding-3-small" || body["dimensions"] != float64(2) {
		t.Fatalf("body = %#v", body)
	}
	if vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("order = %v", vecs)
	}
	t.Setenv("VOYAGE_API_KEY", "")
	if _, err := New(config.Embedder{Provider: "voyage", Dimensions: 2, Model: "m"}); err == nil || !contains(err.Error(), "api key") {
		t.Fatalf("voyage key err = %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := New(config.Embedder{Provider: "openai", Model: "m", Dimensions: 2}); err == nil {
		t.Fatal("expected missing key")
	}
	if vecs, err := emb.EmbedBatch(context.Background(), nil); err != nil || len(vecs) != 0 {
		t.Fatalf("empty = %v %v", vecs, err)
	}
	_ = os.Getenv
}

func TestRetry429ThenOK(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 0, 0, 0}}})
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{Provider: "ollama", Endpoint: srv.URL, Model: "m", Dimensions: 4})
	if err != nil {
		t.Fatal(err)
	}
	got, err := emb.Embed(context.Background(), "hello")
	if err != nil || len(got) != 4 {
		t.Fatalf("%v %v", got, err)
	}
	if n != 3 {
		t.Fatalf("posts = %d", n)
	}
}

func TestRetry503ThenOK(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 0}}})
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{Provider: "ollama", Endpoint: srv.URL, Model: "m", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emb.Embed(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("posts = %d", n)
	}
}

func TestRetryGivesUp(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{Provider: "ollama", Endpoint: srv.URL, Model: "m", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = emb.Embed(context.Background(), "hello")
	if err == nil || n != 6 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestNoRetryOn400(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{Provider: "ollama", Endpoint: srv.URL, Model: "m", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = emb.Embed(context.Background(), "hello")
	if err == nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestVoyageQueryAndDocument(t *testing.T) {
	var auth string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		if body["input_type"] == "document" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"index": 1, "embedding": []float32{0, 1}},
					{"index": 0, "embedding": []float32{1, 0}},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
		})
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{
		Provider: "voyage", Endpoint: srv.URL, Model: "voyage-code-3",
		Dimensions: 2, APIKey: "vk",
	})
	if err != nil {
		t.Fatal(err)
	}
	vec, err := emb.Embed(context.Background(), "q")
	if err != nil || len(vec) != 2 || vec[0] != 1 {
		t.Fatalf("query %v %v", vec, err)
	}
	vecs, err := emb.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil || len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("batch %v %v", vecs, err)
	}
	if auth != "Bearer vk" {
		t.Fatalf("auth = %s", auth)
	}
	if bodies[0]["input_type"] != "query" || bodies[0]["output_dimension"] != float64(2) {
		t.Fatalf("query body = %#v", bodies[0])
	}
	if bodies[1]["input_type"] != "document" || bodies[1]["model"] != "voyage-code-3" {
		t.Fatalf("batch body = %#v", bodies[1])
	}
	empty, err := emb.EmbedBatch(context.Background(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty = %v %v", empty, err)
	}

	t.Setenv("VOYAGE_API_KEY", "")
	if _, err := New(config.Embedder{Provider: "voyage", Endpoint: srv.URL, Model: "voyage-code-3", Dimensions: 2}); err == nil || !contains(err.Error(), "api key") {
		t.Fatalf("missing key = %v", err)
	}
	t.Setenv("VOYAGE_API_KEY", "from-env")
	var envAuth string
	envSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
		})
	}))
	defer envSrv.Close()
	envEmb, err := New(config.Embedder{Provider: "voyage", Endpoint: envSrv.URL, Model: "voyage-code-3", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := envEmb.Embed(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	if envAuth != "Bearer from-env" {
		t.Fatalf("env auth = %s", envAuth)
	}
}

func TestVoyageRetry429(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 0}}},
		})
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{Provider: "voyage", Endpoint: srv.URL, Model: "voyage-code-3", Dimensions: 2, APIKey: "vk"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := emb.Embed(context.Background(), "hello")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if n != 2 {
		t.Fatalf("posts = %d", n)
	}
}

func TestVoyageNoRetryOn400(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Error(w, "bad model", http.StatusBadRequest)
	}))
	defer srv.Close()
	emb, err := New(config.Embedder{Provider: "voyage", Endpoint: srv.URL, Model: "voyage-code-3", Dimensions: 2, APIKey: "vk"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = emb.Embed(context.Background(), "hello")
	if err == nil || !contains(err.Error(), "embedder: 400") {
		t.Fatalf("err = %v", err)
	}
	if n != 1 {
		t.Fatalf("posts = %d", n)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRetryable(t *testing.T) {
	for status, want := range map[int]bool{429: true, 502: true, 503: true, 504: true, 500: false, 400: false, 401: false} {
		if got := retryable(&statusError{Status: status}); got != want {
			t.Fatalf("%d: %v", status, got)
		}
	}
	if retryable(errors.New("boom")) {
		t.Fatal("plain error retried")
	}
}

type recordEmb struct{ got []string }

func (r *recordEmb) Embed(_ context.Context, t string) ([]float32, error) {
	r.got = append(r.got, t)
	return []float32{1}, nil
}
func (r *recordEmb) EmbedBatch(_ context.Context, ts []string) ([][]float32, error) {
	r.got = append(r.got, ts...)
	out := make([][]float32, len(ts))
	for i := range out {
		out[i] = []float32{1}
	}
	return out, nil
}

func TestWithPrefixesSplitsQueriesFromDocuments(t *testing.T) {
	r := &recordEmb{}
	e := WithPrefixes(r, "q: ", "d: ")
	e.Embed(context.Background(), "find")
	e.EmbedBatch(context.Background(), []string{"a", "b"})
	want := []string{"q: find", "d: a", "d: b"}
	if len(r.got) != 3 || r.got[0] != want[0] || r.got[1] != want[1] || r.got[2] != want[2] {
		t.Fatalf("%v", r.got)
	}
	if WithPrefixes(r, "", "") != Embedder(r) {
		t.Fatal("no prefixes must return the embedder unchanged")
	}
}

func TestOllamaSendsContextWindowForLongInputs(t *testing.T) {
	if numCtxFor(0) != 0 || numCtxFor(3000) != 0 || numCtxFor(6000) != 6512 {
		t.Fatalf("%d %d %d", numCtxFor(0), numCtxFor(3000), numCtxFor(6000))
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Options map[string]any `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		got = in.Options
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 2}}})
	}))
	defer srv.Close()
	e, err := New(config.Embedder{Provider: "ollama", Endpoint: srv.URL, Model: "m", Dimensions: 2, MaxInputTokens: 6000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(context.Background(), "x"); err != nil || got["num_ctx"] != float64(6512) {
		t.Fatalf("options %v err %v", got, err)
	}
}

func TestOpenAIEmbedderLocalServerNeedsNoKeyAndNoDimensions(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float32{1, 2}}}})
	}))
	defer srv.Close()
	e, err := New(config.Embedder{Provider: "openai", Endpoint: srv.URL, Model: "m", Dimensions: 2})
	if err != nil {
		t.Fatalf("a custom endpoint needs no key: %v", err)
	}
	if _, err := e.Embed(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if _, sent := body["dimensions"]; sent {
		t.Fatalf("dimensions must not be sent to a custom endpoint: %v", body)
	}
	yes := true
	e, _ = New(config.Embedder{Provider: "openai", Endpoint: srv.URL, Model: "m", Dimensions: 2, SendDimensions: &yes})
	if _, err := e.Embed(context.Background(), "x"); err != nil || body["dimensions"] != float64(2) {
		t.Fatalf("send_dimensions must be honoured: %v %v", body, err)
	}
	if _, err := New(config.Embedder{Provider: "openai", Model: "m", Dimensions: 2}); err == nil {
		t.Fatal("the hosted endpoint needs a key")
	}
}

func TestQueryPrefixDefaultsToTheQwen3Instruction(t *testing.T) {
	if got := queryPrefix(config.Embedder{Model: "qwen3-embedding:0.6b"}); got != qwen3Instruct {
		t.Fatalf("qwen3 = %q", got)
	}
	if got := queryPrefix(config.Embedder{Model: "Qwen3-Embedding-4B", QueryPrefix: "search: "}); got != "search: " {
		t.Fatalf("an explicit prefix wins: %q", got)
	}
	if got := queryPrefix(config.Embedder{Model: "nomic-embed-text"}); got != "" {
		t.Fatalf("other models get none: %q", got)
	}
}

type deadline struct{}

func (deadline) Error() string   { return "deadline" }
func (deadline) Timeout() bool   { return true }
func (deadline) Temporary() bool { return true }

func TestATimeoutIsNotRetriedWholeButIsReported(t *testing.T) {
	if retryable(deadline{}) || !IsTimeout(deadline{}) || IsTimeout(errors.New("boom")) {
		t.Fatal("a timed-out batch goes back to the caller to be split")
	}
}

func TestTimeoutAndPrefixComeFromTheProfileOrTheConfiguration(t *testing.T) {
	if got := newHTTPFor(0).Timeout; got != DefaultTimeout {
		t.Fatalf("%v", got)
	}
	if got := newHTTPFor(7).Timeout; got != 7*time.Second {
		t.Fatalf("embedder.timeout_seconds: %v", got)
	}
	if queryPrefix(config.Embedder{Model: "Qwen3-Embedding:0.6b"}) != qwen3Instruct || queryPrefix(config.Embedder{Model: "nomic-embed-text"}) != "" {
		t.Fatal("the profile of the model gives the prefix")
	}
	if queryPrefix(config.Embedder{Model: "qwen3-embedding", QueryPrefix: "q: "}) != "q: " {
		t.Fatal("embedder.query_prefix wins")
	}
}
