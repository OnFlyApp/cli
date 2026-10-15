package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/ignore"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/tokens"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0, 1}, nil
}

func (fakeEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0, 1}
	}
	return out, nil
}

func TestTools(t *testing.T) {
	root := t.TempDir()
	rules := filepath.Join(root, "rules.md")
	if err := os.WriteFile(rules, []byte("use errors"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(context.Background(), store, []vectorstore.EmbeddedChunk{{
		Chunk: parser.Chunk{
			FilePath: "src/a.go",
			Type:     "function",
			Content:  "func getUserId() {}",
			Metadata: map[string]string{"name": "User"},
		},
		Vector: []float32{1, 0, 0, 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ProjectRoot: root, Rules: []string{rules}}
	srv, cleanup, err := NewServer(cfg, store, fakeEmbedder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "search_keyword",
		Arguments: map[string]any{"keyword": "getuserid"},
	})
	if err != nil || res.IsError {
		t.Fatalf("keyword %v %+v", err, res)
	}
	if !strings.Contains(mustJSON(structured(res)), "getUserId") {
		t.Fatalf("%s", mustJSON(structured(res)))
	}
	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "who is the user", "limit": 100},
	})
	if err != nil || res.IsError {
		t.Fatalf("semantic %v %+v", err, res)
	}
	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "get_component_ast",
		Arguments: map[string]any{"name": "Missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("missing component should be a tool error")
	}
	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "get_project_rules",
		Arguments: map[string]any{},
	})
	if err != nil || res.IsError || !strings.Contains(mustJSON(structured(res)), "use errors") {
		t.Fatalf("rules %v %+v", err, res)
	}
	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "read_file_raw",
		Arguments: map[string]any{"path": "../rules.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("traversal should be a tool error")
	}
}

func TestServeStdoutIsJSON(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, ".onfly", "rules")
	if err := os.MkdirAll(rules, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rules, "architecture.md"), []byte("keep logs off stdout"), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "project: demo\ndatabase:\n  driver: sqlite\n  dsn: .onfly/vector.db\nembedder:\n  provider: ollama\n  endpoint: http://127.0.0.1:1\n  model: fake\n  dimensions: 4\nrules:\n  - .onfly/rules/architecture.md\nindexer:\n  include: [\"src/**/*\"]\n  parsers:\n    - extensions: [\".go\"]\n      type: stdio\n      command: cat\nserver:\n  transport: stdio\n  log_level: debug\n  log_file: .onfly/server.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".onfly", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	clientToServerR, clientToServerW := io.Pipe()
	serverToClientR, serverToClientW := io.Pipe()
	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, cfg, clientToServerR, io.MultiWriter(&stdout, serverToClientW))
	}()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.IOTransport{Reader: serverToClientR, Writer: clientToServerW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "get_project_rules", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("rules %v %+v", err, res)
	}
	session.Close()
	cancel()
	_ = clientToServerW.Close()
	<-errCh

	scan := bufio.NewScanner(&stdout)
	lines := 0
	for scan.Scan() {
		line := bytes.TrimSpace(scan.Bytes())
		if len(line) == 0 {
			continue
		}
		lines++
		var msg any
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("stdout is not json: %s", line)
		}
	}
	if lines == 0 {
		t.Fatal("no stdout frames")
	}
	if strings.Contains(stdout.String(), "onfly serve") {
		t.Fatal("log line leaked to stdout")
	}
	logBody, err := os.ReadFile(cfg.Server.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBody), "onfly serve") {
		t.Fatalf("log file: %s", logBody)
	}
}

func repeatTokens(t *testing.T, word string, n int) string {
	t.Helper()
	var b strings.Builder
	for {
		b.WriteString(word)
		c, err := tokens.Count(b.String())
		if err != nil {
			t.Fatal(err)
		}
		if c >= n {
			return b.String()
		}
	}
}

func openServer(t *testing.T, cfg config.Config, store vectorstore.Store, emb embedder.Embedder) *sdkmcp.ClientSession {
	t.Helper()
	return openSession(t, cfg, store, emb, nil)
}

func openSession(t *testing.T, cfg config.Config, store vectorstore.Store, emb embedder.Embedder, opts *sdkmcp.ClientOptions) *sdkmcp.ClientSession {
	t.Helper()
	srv, cleanup, err := NewServer(cfg, store, emb, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, opts)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func seedUsers(t *testing.T) *vectorstore.SQLite {
	t.Helper()
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := store.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(ctx, store, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "backend/user.go", Type: "function", Content: "func User() {}", Metadata: map[string]string{"name": "User"}}, Vector: []float32{1, 0, 0, 0}, Workspace: "backend"},
		{Chunk: parser.Chunk{FilePath: "frontend/user.go", Type: "function", Content: "func User() {}", Metadata: map[string]string{"name": "User"}}, Vector: []float32{0, 1, 0, 0}, Workspace: "frontend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type countEmbedder struct{ n int }

func (e *countEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.n++
	return []float32{0, 1, 0, 0}, nil
}
func (e *countEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

type fixedEmbedder struct{ v []float32 }

func (e fixedEmbedder) Embed(context.Context, string) ([]float32, error) { return e.v, nil }
func (e fixedEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func decodeList(t *testing.T, v any, dest any) {
	t.Helper()
	raw := []byte(mustJSON(v))
	if len(raw) > 0 && raw[0] == '{' {
		var box map[string]json.RawMessage
		if err := json.Unmarshal(raw, &box); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"hits", "chunks", "entries"} {
			if inner, ok := box[key]; ok {
				raw = inner
				break
			}
		}
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		t.Fatal(err)
	}
}

func decodeHits(t *testing.T, v any) []hitDTO {
	t.Helper()
	var got []hitDTO
	decodeList(t, v, &got)
	return got
}

func decodeChunks(t *testing.T, v any) []chunkDTO {
	t.Helper()
	var got []chunkDTO
	decodeList(t, v, &got)
	return got
}

func TestSemanticBudgetDropsTail(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	texts := []string{repeatTokens(t, "a", 100), repeatTokens(t, "b", 100), repeatTokens(t, "c", 100)}
	vecs := [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	names := []string{"A", "B", "C"}
	var saved []vectorstore.EmbeddedChunk
	for i := range texts {
		saved = append(saved, vectorstore.EmbeddedChunk{
			Chunk: parser.Chunk{
				FilePath: names[i] + ".go",
				Type:     "function",
				Content:  texts[i],
				Metadata: map[string]string{"name": names[i]},
			},
			Vector: vecs[i],
		})
	}
	if err := storetest.Save(context.Background(), store, saved); err != nil {
		t.Fatal(err)
	}
	one, err := tokens.Count(mustJSON([]hitDTO{toHit(saved[0].Chunk), diagnosticHit(2)}))
	if err != nil {
		t.Fatal(err)
	}
	session := openServer(t, config.Config{Server: config.Server{MaxTokens: one + 8}}, store, fixedEmbedder{v: []float32{1, 0, 0, 0}})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "a", "limit": 10},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	got := decodeHits(t, structured(res))
	if len(got) != 2 || got[0].Symbol != "A" {
		t.Fatalf("%+v", got)
	}
	want := "// [Truncated: 2 matches omitted due to context limits.]"
	if got[1].Lines != want {
		t.Fatalf("diagnostic = %q", got[1].Lines)
	}

	tight := openServer(t, config.Config{Server: config.Server{MaxTokens: 8}}, store, fixedEmbedder{v: []float32{1, 0, 0, 0}})
	res, err = tight.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "a", "limit": 10},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	got = decodeHits(t, structured(res))
	if len(got) != 1 || got[0].Lines != "// [Truncated: 3 matches omitted due to context limits.]" {
		t.Fatalf("%+v", got)
	}
}

func TestGetReferences(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(context.Background(), store, []vectorstore.EmbeddedChunk{{
		Chunk: parser.Chunk{
			FilePath: "src/order.go",
			Type:     "function",
			Content:  "func run() {}",
			Outbound: []string{"User"},
			Metadata: map[string]string{"name": "Order"},
		},
		Vector: []float32{1, 0, 0, 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	session := openServer(t, config.Config{}, store, fakeEmbedder{})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "get_references",
		Arguments: map[string]any{"name": "User"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	got := decodeChunks(t, structured(res))
	if len(got) != 1 || got[0].FilePath != "src/order.go" {
		t.Fatalf("%+v", got)
	}
	res, err = session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "get_references",
		Arguments: map[string]any{"name": "Missing"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if len(decodeChunks(t, structured(res))) != 0 {
		t.Fatalf("%s", mustJSON(structured(res)))
	}
}

func TestWorkspaceArg(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(context.Background(), store, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "back.go", Type: "function", Content: "alpha backend", Metadata: map[string]string{"name": "Back"}}, Vector: []float32{1, 0, 0, 1}, Workspace: "backend"},
		{Chunk: parser.Chunk{FilePath: "front.go", Type: "function", Content: "alpha frontend", Metadata: map[string]string{"name": "Front"}}, Vector: []float32{0, 1, 0, 1}, Workspace: "frontend"},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := openServer(t, config.Config{}, store, fakeEmbedder{})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search_keyword",
		Arguments: map[string]any{"keyword": "alpha", "workspace": "frontend"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	got := decodeChunks(t, structured(res))
	if len(got) != 1 || got[0].FilePath != "front.go" {
		t.Fatalf("%+v", got)
	}
}

func TestTraceLine(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(context.Background(), store, []vectorstore.EmbeddedChunk{{
		Chunk:  parser.Chunk{FilePath: "a.go", Type: "function", Content: "func getUserId() {}", Metadata: map[string]string{"name": "User"}},
		Vector: []float32{1, 0, 0, 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(t.TempDir(), "mcp_trace.jsonl")
	session := openServer(t, config.Config{Server: config.Server{TraceFile: tracePath}}, store, fakeEmbedder{})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search_keyword",
		Arguments: map[string]any{"keyword": "getuserid"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	body, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d\n%s", len(lines), body)
	}
	var rec struct {
		Tool   string `json:"tool"`
		Chunks int    `json:"chunks"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Tool != "search_keyword" || rec.Chunks < 1 {
		t.Fatalf("%+v", rec)
	}
}

func TestGenerationNotify(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 4)
	srv, cleanup, err := NewServer(config.Config{}, store, fakeEmbedder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, &sdkmcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *sdkmcp.ResourceUpdatedNotificationRequest) {
			got <- req.Params.URI
		},
	})
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Subscribe(ctx, &sdkmcp.SubscribeParams{URI: "onfly://index"}); err != nil {
		t.Fatal(err)
	}
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go pollGeneration(pollCtx, store, func() error {
		return srv.ResourceUpdated(pollCtx, &sdkmcp.ResourceUpdatedNotificationParams{URI: "onfly://index"})
	}, 30*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	select {
	case uri := <-got:
		t.Fatalf("early notify %s", uri)
	default:
	}
	err = storetest.Save(ctx, store, []vectorstore.EmbeddedChunk{{
		Chunk:  parser.Chunk{FilePath: "a.go", Type: "function", Content: "func a() {}", Metadata: map[string]string{"name": "A"}},
		Vector: []float32{1, 0, 0, 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case uri := <-got:
		if uri != "onfly://index" {
			t.Fatalf("uri = %s", uri)
		}
	case <-time.After(time.Second):
		t.Fatal("no notify")
	}
	select {
	case uri := <-got:
		t.Fatalf("second notify %s", uri)
	case <-time.After(120 * time.Millisecond):
	}
}

func TestSemanticElicitsCollidingName(t *testing.T) {
	store := seedUsers(t)
	emb := &countEmbedder{}
	session := openSession(t, config.Config{}, store, emb, &sdkmcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, req *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
			if req.Params.Message != `Which workspace should be searched for "User"?` {
				t.Fatalf("message %q", req.Params.Message)
			}
			return &sdkmcp.ElicitResult{Action: "accept", Content: map[string]any{"workspace": "frontend"}}, nil
		},
	})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "User"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if emb.n != 1 {
		t.Fatalf("embeds = %d", emb.n)
	}
	got := decodeChunks(t, structured(res))
	if len(got) != 1 || got[0].FilePath != "frontend/user.go" {
		t.Fatalf("%+v", got)
	}
	info := session.InitializeResult().ServerInfo
	if info == nil || info.Name != "onfly" || info.Version != Version {
		t.Fatalf("%+v", info)
	}
}

func TestSemanticDeclineDoesNotEmbed(t *testing.T) {
	store := seedUsers(t)
	emb := &countEmbedder{}
	session := openSession(t, config.Config{}, store, emb, &sdkmcp.ClientOptions{
		ElicitationHandler: func(context.Context, *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
			return &sdkmcp.ElicitResult{Action: "decline"}, nil
		},
	})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "User"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(mustJSON(res), "workspace is required") {
		t.Fatalf("%s", mustJSON(res))
	}
	if emb.n != 0 {
		t.Fatalf("embeds = %d", emb.n)
	}
}

func TestSemanticStarDoesNotElicit(t *testing.T) {
	store := seedUsers(t)
	emb := &countEmbedder{}
	session := openSession(t, config.Config{}, store, emb, &sdkmcp.ClientOptions{
		ElicitationHandler: func(context.Context, *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
			t.Fatal("elicited")
			return nil, nil
		},
	})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "User", "workspace": "*"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if emb.n != 1 {
		t.Fatalf("embeds = %d", emb.n)
	}
}

func TestSemanticSentenceDoesNotElicit(t *testing.T) {
	store := seedUsers(t)
	emb := &countEmbedder{}
	session := openSession(t, config.Config{Workspace: "backend"}, store, emb, &sdkmcp.ClientOptions{
		ElicitationHandler: func(context.Context, *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
			t.Fatal("elicited")
			return nil, nil
		},
	})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "who is the user"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	got := decodeChunks(t, structured(res))
	if len(got) != 1 || got[0].FilePath != "backend/user.go" {
		t.Fatalf("%+v", got)
	}
	if emb.n != 1 {
		t.Fatalf("embeds = %d", emb.n)
	}
}

func TestComponentASTIsObject(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := store.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(ctx, store, []vectorstore.EmbeddedChunk{{
		Chunk:  parser.Chunk{FilePath: "src/a.go", Type: "function", Content: "func User() {}", Metadata: map[string]string{"name": "User"}},
		Vector: []float32{1, 0, 0, 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	session := openServer(t, config.Config{}, store, fakeEmbedder{})
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "get_component_ast",
		Arguments: map[string]any{"name": "User"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	var dto chunkDTO
	if err := json.Unmarshal([]byte(mustJSON(structured(res))), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.FilePath != "src/a.go" || dto.Type != "function" || dto.Metadata["name"] != "User" {
		t.Fatalf("%+v", dto)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.HasPrefix(strings.TrimSpace(text.Text), "{") {
		t.Fatalf("%+v", res.Content)
	}
}

func TestSearchToolsAndFusion(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := store.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(ctx, store, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{
			FilePath: "far.go", Type: "method", Content: "func getUserId() {}",
			Metadata: map[string]string{"name": "getUserId", "receiver": "Service", "start_line": "4", "end_line": "4"},
		}, Vector: []float32{0, 1, 0, 0}},
		{Chunk: parser.Chunk{
			FilePath: "near.go", Type: "function", Content: "func other() {}",
			Metadata: map[string]string{"name": "other"},
		}, Vector: []float32{1, 0, 0, 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	emb := &countEmbedder{}
	session := openServer(t, config.Config{}, store, fixedEmbedder{v: []float32{1, 0, 0, 0}})
	listed, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	if !names["search"] || !names["search_keyword"] || names["search_semantic"] {
		t.Fatalf("%v", names)
	}
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "getUserId", "limit": 2},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	raw := mustJSON(structured(res))
	var wrapped struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		t.Fatal(err)
	}
	rows := wrapped.Hits
	if len(rows) != 2 || rows[0]["file_path"] != "far.go" || rows[1]["file_path"] != "near.go" {
		t.Fatalf("%s", raw)
	}
	if rows[0]["symbol"] != "getUserId" || rows[0]["receiver"] != "Service" || rows[0]["start_line"] != float64(4) || rows[0]["lines"] != "func getUserId() {}" {
		t.Fatalf("%s", raw)
	}
	for _, key := range []string{"type", "metadata", "outbound", "receiver"} {
		if _, ok := rows[1][key]; ok {
			t.Fatalf("near has %s: %s", key, raw)
		}
	}
	if _, ok := rows[0]["type"]; ok || rows[0]["metadata"] != nil || rows[0]["outbound"] != nil {
		t.Fatalf("%s", raw)
	}
	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "search_keyword",
		Arguments: map[string]any{"keyword": "getUserId"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	hits := decodeHits(t, structured(res))
	if len(hits) != 1 || hits[0].FilePath != "far.go" || hits[0].Lines != "func getUserId() {}" {
		t.Fatalf("%+v", hits)
	}
	blank := openServer(t, config.Config{}, store, emb)
	res, err = blank.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "  "},
	})
	if err != nil || !res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if emb.n != 0 {
		t.Fatalf("embeds = %d", emb.n)
	}
}

func TestRulesFollowTheRequest(t *testing.T) {
	root := t.TempDir()
	rulesDir := filepath.Join(root, ".onfly", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(rulesDir, "db.md"):    "---\nname: Database\nscope: [\"**/*.go\"]\npriority: 100\n---\nNO RAW SQL\n",
		filepath.Join(rulesDir, "iface.md"): "---\nname: Interfaces\nscope: [\"interface\"]\npriority: 80\n---\nDOCUMENT THE INTERFACE\n",
		filepath.Join(rulesDir, "all.md"):   "---\nname: Always\nscope: [\"*\"]\npriority: 1\n---\nALWAYS ON\n",
		filepath.Join(root, "src", "a.go"):  "package a\n",
		filepath.Join(root, "README.md"):    "readme\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(context.Background(), store, []vectorstore.EmbeddedChunk{{
		Chunk: parser.Chunk{
			FilePath: "src/a.go",
			Type:     "interface",
			Content:  "type Widget interface {}",
			Metadata: map[string]string{"name": "Widget"},
		},
		Vector: []float32{1, 0, 0, 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	session := openSession(t, config.Config{ProjectRoot: root, Server: config.Server{MaxTokens: 8192}}, store, fakeEmbedder{}, nil)
	ctx := context.Background()

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "read_file_raw", Arguments: map[string]any{"path": "src/a.go"}})
	if err != nil || res.IsError {
		t.Fatalf("read go %v %+v", err, res)
	}
	if !strings.Contains(contentText(t, res), "NO RAW SQL") || !strings.Contains(contentText(t, res), "ALWAYS ON") {
		t.Fatalf("go file rules: %s", contentText(t, res))
	}
	if !strings.Contains(mustJSON(structured(res)), "NO RAW SQL") || !strings.Contains(mustJSON(structured(res)), "ALWAYS ON") {
		t.Fatalf("structured %s", mustJSON(structured(res)))
	}
	if instr := session.InitializeResult().Instructions; !strings.Contains(instr, "ALWAYS ON") || strings.Contains(instr, "NO RAW SQL") {
		t.Fatalf("instructions %s", instr)
	}

	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "read_file_raw", Arguments: map[string]any{"path": "README.md"}})
	if err != nil || res.IsError {
		t.Fatalf("read md %v %+v", err, res)
	}
	readme := contentText(t, res)
	if strings.Contains(readme, "NO RAW SQL") || !strings.Contains(readme, "ALWAYS ON") {
		t.Fatalf("readme rules: %s", readme)
	}

	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "get_component_ast", Arguments: map[string]any{"name": "Widget"}})
	if err != nil || res.IsError {
		t.Fatalf("ast %v %+v", err, res)
	}
	ast := contentText(t, res)
	if !strings.Contains(ast, "DOCUMENT THE INTERFACE") || !strings.Contains(ast, "NO RAW SQL") {
		t.Fatalf("ast rules: %s", ast)
	}

	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "get_project_rules", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("rules %v %+v", err, res)
	}
	body := mustJSON(structured(res))
	if !strings.Contains(body, "ALWAYS ON") || strings.Contains(body, "NO RAW SQL") {
		t.Fatalf("project rules: %s", body)
	}

	missing := openSession(t, config.Config{ProjectRoot: root, Rules: []string{filepath.Join(root, "missing.md")}, Server: config.Server{MaxTokens: 8192}}, store, fakeEmbedder{}, nil)
	res, err = missing.CallTool(ctx, &sdkmcp.CallToolParams{Name: "get_project_rules", Arguments: map[string]any{}})
	if err != nil || !res.IsError {
		t.Fatalf("missing rule %v %+v", err, res)
	}
}

func contentText(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, item := range res.Content {
		text, ok := item.(*sdkmcp.TextContent)
		if !ok {
			t.Fatalf("%T", item)
		}
		b.WriteString(text.Text)
	}
	return b.String()
}

func TestReadAndListHonourGitignoreAndExclude(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		".gitignore":  "secret.txt\n",
		"secret.txt":  "hidden",
		"keep.txt":    "ok",
		"gen/out.txt": "generated",
		"id_ed25519":  "key",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &tools{root: root}
	filter, err := ignore.LoadFilter(root, []string{"gen"})
	if err != nil {
		t.Fatal(err)
	}
	h.filter = filter
	for _, bad := range []string{"secret.txt", "gen/out.txt", "id_ed25519"} {
		if _, err := h.readFileBody(pathIn{Path: bad}); err == nil {
			t.Fatalf("%s was readable", bad)
		}
	}
	if out, err := h.readFileBody(pathIn{Path: "keep.txt"}); err != nil || out.Content != "ok" {
		t.Fatalf("%+v %v", out, err)
	}
	entries, err := h.listDirBody(pathIn{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "secret.txt" || e.Name == "gen" || e.Name == "id_ed25519" {
			t.Fatalf("listed %s", e.Name)
		}
	}
}

func TestComponentReceiverDisambiguation(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := store.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	err = storetest.Save(ctx, store, []vectorstore.EmbeddedChunk{
		{Chunk: parser.Chunk{FilePath: "u.go", Type: "method", Content: "func (u User) Save() {}", Metadata: map[string]string{"name": "Save", "receiver": "User", "start_line": "3"}}, Vector: []float32{1, 0, 0, 0}},
		{Chunk: parser.Chunk{FilePath: "o.go", Type: "method", Content: "func (o Order) Save() {}", Metadata: map[string]string{"name": "Save", "receiver": "Order", "start_line": "7"}}, Vector: []float32{1, 0, 0, 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := openServer(t, config.Config{}, store, fakeEmbedder{})
	call := func(args map[string]any) chunkDTO {
		t.Helper()
		res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "get_component_ast", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%v %+v", err, res)
		}
		var dto chunkDTO
		if err := json.Unmarshal([]byte(mustJSON(structured(res))), &dto); err != nil {
			t.Fatal(err)
		}
		return dto
	}
	dto := call(map[string]any{"name": "Save"})
	if len(dto.Alternatives) != 1 || dto.Alternatives[0].Receiver != "Order" || dto.Alternatives[0].StartLine != 7 {
		t.Fatalf("%+v", dto)
	}
	dto = call(map[string]any{"name": "Save", "receiver": "Order"})
	if dto.Metadata["receiver"] != "Order" || len(dto.Alternatives) != 0 {
		t.Fatalf("%+v", dto)
	}
}

func TestComponentAlternativesAreCapped(t *testing.T) {
	store, err := vectorstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := store.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	var chunks []vectorstore.EmbeddedChunk
	for i := 0; i < 25; i++ {
		f := fmt.Sprintf("f%02d.go", i)
		chunks = append(chunks, vectorstore.EmbeddedChunk{Chunk: parser.Chunk{FilePath: f, Type: "function", Content: "func init() { /*" + f + "*/ }", Metadata: map[string]string{"name": "init"}}, Vector: []float32{1, 0, 0, 0}})
	}
	if err := storetest.Save(ctx, store, chunks); err != nil {
		t.Fatal(err)
	}
	session := openServer(t, config.Config{}, store, fakeEmbedder{})
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "get_component_ast", Arguments: map[string]any{"name": "init"}})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	var dto chunkDTO
	if err := json.Unmarshal([]byte(mustJSON(structured(res))), &dto); err != nil {
		t.Fatal(err)
	}
	if len(dto.Alternatives) != 20 || dto.AlternativesOmitted != 4 {
		t.Fatalf("alts=%d omitted=%d", len(dto.Alternatives), dto.AlternativesOmitted)
	}
}

func TestNewServerDegradesWithoutRerankerKey(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	cfg := config.Config{}
	cfg.Reranker.Provider = "voyage"
	cfg.Reranker.Model = "rerank-2"
	store := seedUsers(t)
	_, cleanup, err := NewServer(cfg, store, fakeEmbedder{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
}

// structured returns a tool result as a JSON object: the last line of the
// text block, plus "rules" from the lines above it when there are any.
func structured(res *sdkmcp.CallToolResult) any {
	if res == nil || len(res.Content) == 0 {
		return nil
	}
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		return nil
	}
	text := tc.Text
	rules := ""
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		rules, text = text[:i], text[i+1:]
	}
	var m any
	if json.Unmarshal([]byte(text), &m) != nil {
		return nil
	}
	if obj, ok := m.(map[string]any); ok && rules != "" {
		obj["rules"] = rules
	}
	return m
}
