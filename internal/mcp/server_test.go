package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/tokens"
	"github.com/axitdev/onfly/internal/vectorstore"
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
	err = store.SaveChunks(context.Background(), []vectorstore.EmbeddedChunk{{
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
	if !strings.Contains(mustJSON(res.StructuredContent), "getUserId") {
		t.Fatalf("%s", mustJSON(res.StructuredContent))
	}
	res, err = session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "search_semantic",
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
	if err != nil || res.IsError || !strings.Contains(mustJSON(res.StructuredContent), "use errors") {
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
	err = store.SaveChunks(ctx, []vectorstore.EmbeddedChunk{
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

func decodeChunks(t *testing.T, v any) []chunkDTO {
	t.Helper()
	var got []chunkDTO
	if err := json.Unmarshal([]byte(mustJSON(v)), &got); err != nil {
		t.Fatal(err)
	}
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
	if err := store.SaveChunks(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	one, err := tokens.Count(mustJSON(toDTO([]parser.Chunk{saved[0].Chunk, truncation(2)})))
	if err != nil {
		t.Fatal(err)
	}
	session := openServer(t, config.Config{Server: config.Server{MaxTokens: one + 8}}, store, fixedEmbedder{v: []float32{1, 0, 0, 0}})
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search_semantic",
		Arguments: map[string]any{"query": "a", "limit": 10},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	got := decodeChunks(t, res.StructuredContent)
	if len(got) != 2 || got[0].Metadata["name"] != "A" {
		t.Fatalf("%+v", got)
	}
	want := "// [Truncated: 2 matches omitted due to context limits. Use search_keyword to refine.]"
	if got[1].Content != want {
		t.Fatalf("diagnostic = %q", got[1].Content)
	}

	tight := openServer(t, config.Config{Server: config.Server{MaxTokens: 8}}, store, fixedEmbedder{v: []float32{1, 0, 0, 0}})
	res, err = tight.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "search_semantic",
		Arguments: map[string]any{"query": "a", "limit": 10},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	got = decodeChunks(t, res.StructuredContent)
	if len(got) != 1 || got[0].Content != "// [Truncated: 3 matches omitted due to context limits. Use search_keyword to refine.]" {
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
	err = store.SaveChunks(context.Background(), []vectorstore.EmbeddedChunk{{
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
	got := decodeChunks(t, res.StructuredContent)
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
	if len(decodeChunks(t, res.StructuredContent)) != 0 {
		t.Fatalf("%s", mustJSON(res.StructuredContent))
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
	err = store.SaveChunks(context.Background(), []vectorstore.EmbeddedChunk{
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
	got := decodeChunks(t, res.StructuredContent)
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
	err = store.SaveChunks(context.Background(), []vectorstore.EmbeddedChunk{{
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
	err = store.SaveChunks(ctx, []vectorstore.EmbeddedChunk{{
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
		Name:      "search_semantic",
		Arguments: map[string]any{"query": "User"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if emb.n != 1 {
		t.Fatalf("embeds = %d", emb.n)
	}
	got := decodeChunks(t, res.StructuredContent)
	if len(got) != 1 || got[0].FilePath != "frontend/user.go" {
		t.Fatalf("%+v", got)
	}
	info := session.InitializeResult().ServerInfo
	if info == nil || info.Name != "onfly" || info.Version != "0.3.0" {
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
		Name:      "search_semantic",
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
		Name:      "search_semantic",
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
		Name:      "search_semantic",
		Arguments: map[string]any{"query": "who is the user"},
	})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	got := decodeChunks(t, res.StructuredContent)
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
	err = store.SaveChunks(ctx, []vectorstore.EmbeddedChunk{{
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
	if err := json.Unmarshal([]byte(mustJSON(res.StructuredContent)), &dto); err != nil {
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
