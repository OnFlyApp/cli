package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/ignore"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/tokens"
	"github.com/axitdev/onfly/internal/vectorstore"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxRawBytes     = 1 << 20
	keywordLimit    = 20
	defaultSemLimit = 10
	maxSemLimit     = 50
)

var componentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type chunkDTO struct {
	FilePath string            `json:"file_path"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Outbound []string          `json:"outbound,omitempty"`
}

type semanticIn struct {
	Query     string `json:"query" jsonschema:"natural language query"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum results, from 1 to 50"`
	Workspace string `json:"workspace,omitempty" jsonschema:"workspace name, or * for every workspace"`
}

type keywordIn struct {
	Keyword   string `json:"keyword" jsonschema:"case-insensitive substring"`
	Workspace string `json:"workspace,omitempty" jsonschema:"workspace name, or * for every workspace"`
}

type nameIn struct {
	Name string `json:"name" jsonschema:"component name from chunk metadata"`
}

type pathIn struct {
	Path string `json:"path" jsonschema:"path relative to the project root"`
}

type fileOut struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type dirEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type emptyIn struct{}

type rulesOut struct {
	Rules string `json:"rules"`
}

type tools struct {
	root      string
	rules     []string
	store     vectorstore.Store
	emb       embedder.Embedder
	workspace string
	maxTokens int
	logger    *slog.Logger
	trace     io.Writer
	traceMu   sync.Mutex
}

func NewServer(cfg config.Config, store vectorstore.Store, emb embedder.Embedder, logger *slog.Logger) (*sdkmcp.Server, func(), error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	maxTokens := cfg.Server.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	h := &tools{
		root: cfg.ProjectRoot, rules: cfg.Rules, store: store, emb: emb,
		workspace: cfg.Workspace, maxTokens: maxTokens, logger: logger,
	}
	closer := func() {}
	if cfg.Server.TraceFile != "" {
		f, err := os.OpenFile(cfg.Server.TraceFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, err
		}
		h.trace = f
		closer = func() { f.Close() }
	}
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "onfly", Version: "0.3.0"}, &sdkmcp.ServerOptions{
		Logger:             logger,
		Instructions:       "Search the indexed project. stdout is JSON-RPC only.",
		SubscribeHandler:   func(context.Context, *sdkmcp.SubscribeRequest) error { return nil },
		UnsubscribeHandler: func(context.Context, *sdkmcp.UnsubscribeRequest) error { return nil },
	})
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "search_semantic", Description: "Embed the query and return the nearest chunks."}, h.searchSemantic)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "search_keyword", Description: "Find chunks by case-insensitive substring or full-text search."}, h.searchKeyword)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_component_ast", Description: "Return the chunk whose metadata name matches."}, h.component)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_references", Description: "Return chunks whose outbound references include the name."}, h.references)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "read_file_raw", Description: "Read a project file, up to 1MiB."}, h.readFile)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "list_directory", Description: "List one directory level inside the project."}, h.listDir)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_project_rules", Description: "Return the configured architecture rule files."}, h.projectRules)
	srv.AddResource(&sdkmcp.Resource{URI: "onfly://index", Name: "index", MIMEType: "text/plain"}, func(ctx context.Context, _ *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		n, err := store.Generation(ctx)
		if err != nil {
			return nil, err
		}
		return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{
			URI: "onfly://index", MIMEType: "text/plain", Text: strconv.FormatInt(n, 10),
		}}}, nil
	})
	return srv, closer, nil
}

func Serve(ctx context.Context, cfg config.Config, in io.Reader, out io.Writer) error {
	lf, err := os.OpenFile(cfg.Server.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	prev := log.Writer()
	log.SetOutput(lf)
	defer log.SetOutput(prev)
	logger := slog.New(slog.NewTextHandler(lf, &slog.HandlerOptions{Level: logLevel(cfg.Server.LogLevel)}))
	log.Printf("onfly serve")

	store, err := vectorstore.New(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Init(ctx, cfg.Embedder.Dimensions); err != nil {
		return err
	}
	emb, err := embedder.New(cfg.Embedder)
	if err != nil {
		return err
	}
	srv, closer, err := NewServer(cfg, store, emb, logger)
	if err != nil {
		return err
	}
	defer closer()
	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()
	go pollGeneration(pollCtx, store, func() error {
		return srv.ResourceUpdated(pollCtx, &sdkmcp.ResourceUpdatedNotificationParams{URI: "onfly://index"})
	}, time.Second)
	return srv.Run(ctx, &sdkmcp.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}})
}

func (h *tools) searchSemantic(ctx context.Context, req *sdkmcp.CallToolRequest, in semanticIn) (*sdkmcp.CallToolResult, []chunkDTO, error) {
	start := time.Now()
	if in.Workspace == "" {
		ws, asked, err := workspaceChoice(req)
		if err != nil {
			h.record("search_semantic", in, start, nil, 0)
			return nil, nil, err
		}
		if asked {
			names, err := h.store.WorkspacesForName(ctx, in.Query)
			if err != nil {
				h.record("search_semantic", in, start, nil, 0)
				return nil, nil, err
			}
			if !containsString(names, ws) {
				h.record("search_semantic", in, start, nil, 0)
				return nil, nil, fmt.Errorf("workspace %q is not in the index", ws)
			}
			return h.finishSemantic(ctx, in, ws, start)
		}
		if componentName.MatchString(in.Query) {
			names, err := h.store.WorkspacesForName(ctx, in.Query)
			if err != nil {
				h.record("search_semantic", in, start, nil, 0)
				return nil, nil, err
			}
			if len(names) >= 2 {
				return &sdkmcp.CallToolResult{
					InputRequests: sdkmcp.InputRequestMap{
						"workspace": &sdkmcp.ElicitParams{
							Message: fmt.Sprintf("Which workspace should be searched for %q?", in.Query),
							RequestedSchema: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"workspace": map[string]any{"type": "string", "enum": names},
								},
								"required": []string{"workspace"},
							},
						},
					},
				}, nil, nil
			}
		}
	}
	return h.finishSemantic(ctx, in, h.scope(in.Workspace), start)
}

func (h *tools) finishSemantic(ctx context.Context, in semanticIn, workspace string, start time.Time) (*sdkmcp.CallToolResult, []chunkDTO, error) {
	vec, err := h.emb.Embed(ctx, in.Query)
	if err != nil {
		h.record("search_semantic", in, start, nil, 0)
		return nil, nil, err
	}
	chunks, err := h.store.SearchSemantic(ctx, vec, clampLimit(in.Limit), workspace)
	if err != nil {
		h.record("search_semantic", in, start, nil, 0)
		return nil, nil, err
	}
	dtos, err := fit(chunks, h.maxTokens)
	logged := in
	logged.Workspace = workspace
	h.record("search_semantic", logged, start, dtos, len(dtos))
	return nil, dtos, err
}

func workspaceChoice(req *sdkmcp.CallToolRequest) (string, bool, error) {
	if req == nil || req.Params == nil || len(req.Params.InputResponses) == 0 {
		return "", false, nil
	}
	raw, ok := req.Params.InputResponses["workspace"]
	if !ok {
		return "", false, nil
	}
	er, ok := raw.(*sdkmcp.ElicitResult)
	if !ok || er == nil || er.Action != "accept" {
		return "", false, fmt.Errorf("workspace is required")
	}
	ws, _ := er.Content["workspace"].(string)
	if ws == "" {
		return "", false, fmt.Errorf("workspace is required")
	}
	return ws, true, nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func (h *tools) searchKeyword(ctx context.Context, _ *sdkmcp.CallToolRequest, in keywordIn) (*sdkmcp.CallToolResult, []chunkDTO, error) {
	start := time.Now()
	chunks, err := h.store.SearchKeyword(ctx, in.Keyword, keywordLimit, h.scope(in.Workspace))
	if err != nil {
		h.record("search_keyword", in, start, nil, 0)
		return nil, nil, err
	}
	dtos, err := fit(chunks, h.maxTokens)
	h.record("search_keyword", in, start, dtos, len(dtos))
	return nil, dtos, err
}

func (h *tools) component(ctx context.Context, _ *sdkmcp.CallToolRequest, in nameIn) (*sdkmcp.CallToolResult, chunkDTO, error) {
	start := time.Now()
	chunk, err := h.store.GetComponentAST(ctx, in.Name)
	if errors.Is(err, vectorstore.ErrNotFound) {
		err = fmt.Errorf("component %q not found", in.Name)
	}
	if err != nil {
		h.record("get_component_ast", in, start, nil, 0)
		return nil, chunkDTO{}, err
	}
	dto := toDTO([]parser.Chunk{*chunk})[0]
	h.record("get_component_ast", in, start, dto, 1)
	return nil, dto, nil
}

func (h *tools) references(ctx context.Context, _ *sdkmcp.CallToolRequest, in nameIn) (*sdkmcp.CallToolResult, []chunkDTO, error) {
	start := time.Now()
	chunks, err := h.store.GetReferences(ctx, in.Name)
	if err != nil {
		h.record("get_references", in, start, nil, 0)
		return nil, nil, err
	}
	dtos, err := fit(chunks, h.maxTokens)
	h.record("get_references", in, start, dtos, len(dtos))
	return nil, dtos, err
}

func (h *tools) readFile(_ context.Context, _ *sdkmcp.CallToolRequest, in pathIn) (*sdkmcp.CallToolResult, fileOut, error) {
	start := time.Now()
	out, err := h.readFileBody(in)
	h.record("read_file_raw", in, start, out, 0)
	return nil, out, err
}

func rejectSensitive(root, full string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	rel, err := filepath.Rel(resolvedRoot, full)
	if err != nil {
		return err
	}
	if ignore.Sensitive(filepath.ToSlash(rel)) {
		return fmt.Errorf("path is not readable")
	}
	return nil
}

func (h *tools) readFileBody(in pathIn) (fileOut, error) {
	full, err := SafePath(h.root, in.Path)
	if err != nil {
		return fileOut{}, err
	}
	if err := rejectSensitive(h.root, full); err != nil {
		return fileOut{}, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return fileOut{}, err
	}
	if !info.Mode().IsRegular() {
		return fileOut{}, fmt.Errorf("path is not a regular file")
	}
	f, err := os.Open(full)
	if err != nil {
		return fileOut{}, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return fileOut{}, err
	}
	if info.Size() > maxRawBytes {
		return fileOut{}, fmt.Errorf("file exceeds 1MiB")
	}
	body, err := io.ReadAll(io.LimitReader(f, maxRawBytes+1))
	if err != nil {
		return fileOut{}, err
	}
	if int64(len(body)) > maxRawBytes {
		return fileOut{}, fmt.Errorf("file exceeds 1MiB")
	}
	return fileOut{Path: in.Path, Content: string(body)}, nil
}

func (h *tools) listDir(_ context.Context, _ *sdkmcp.CallToolRequest, in pathIn) (*sdkmcp.CallToolResult, []dirEntry, error) {
	start := time.Now()
	out, err := h.listDirBody(in)
	h.record("list_directory", in, start, out, 0)
	return nil, out, err
}

func (h *tools) listDirBody(in pathIn) ([]dirEntry, error) {
	request := in.Path
	if request == "" {
		request = "."
	}
	full, err := SafePath(h.root, request)
	if err != nil {
		return nil, err
	}
	if err := rejectSensitive(h.root, full); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]dirEntry, 0, len(entries))
	for _, entry := range entries {
		kind := "file"
		if entry.IsDir() {
			kind = "dir"
		}
		if ignore.Sensitive(entry.Name()) {
			continue
		}
		out = append(out, dirEntry{Name: entry.Name(), Kind: kind})
	}
	return out, nil
}

func (h *tools) projectRules(context.Context, *sdkmcp.CallToolRequest, emptyIn) (*sdkmcp.CallToolResult, rulesOut, error) {
	start := time.Now()
	out, err := h.projectRulesBody()
	h.record("get_project_rules", emptyIn{}, start, out, 0)
	return nil, out, err
}

func (h *tools) projectRulesBody() (rulesOut, error) {
	var b strings.Builder
	for i, path := range h.rules {
		body, err := os.ReadFile(path)
		if err != nil {
			return rulesOut{}, fmt.Errorf("rule %s: %w", path, err)
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "# %s\n\n%s", path, string(body))
	}
	return rulesOut{Rules: b.String()}, nil
}

func (h *tools) scope(arg string) string {
	if arg == "" {
		return h.workspace
	}
	if arg == "*" {
		return ""
	}
	return arg
}

func truncation(omitted int) parser.Chunk {
	return parser.Chunk{
		Type:    "diagnostic",
		Content: fmt.Sprintf("// [Truncated: %d matches omitted due to context limits. Use search_keyword to refine.]", omitted),
	}
}

func fit(chunks []parser.Chunk, maxTokens int) ([]chunkDTO, error) {
	var kept []parser.Chunk
	for _, ch := range chunks {
		trial := append(append([]parser.Chunk{}, kept...), ch)
		omitted := len(chunks) - len(trial)
		payload := trial
		if omitted > 0 {
			payload = append(payload, truncation(omitted))
		}
		n, err := tokens.Count(mustJSON(toDTO(payload)))
		if err != nil {
			return nil, err
		}
		if n > maxTokens {
			break
		}
		kept = trial
	}
	omitted := len(chunks) - len(kept)
	if omitted == 0 {
		return toDTO(kept), nil
	}
	only := toDTO([]parser.Chunk{truncation(len(chunks))})
	if len(kept) == 0 {
		return only, nil
	}
	return toDTO(append(kept, truncation(omitted))), nil
}

func (h *tools) record(tool string, args any, start time.Time, result any, chunks int) {
	if h.trace == nil {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		raw = []byte("null")
	}
	n, err := tokens.Count(string(raw))
	if err != nil {
		n = 0
	}
	rec := struct {
		TS         string `json:"ts"`
		Tool       string `json:"tool"`
		Args       any    `json:"args"`
		DurationMS int64  `json:"duration_ms"`
		Tokens     int    `json:"tokens"`
		Chunks     int    `json:"chunks"`
	}{
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		Tool:       tool,
		Args:       args,
		DurationMS: time.Since(start).Milliseconds(),
		Tokens:     n,
		Chunks:     chunks,
	}
	buf, err := json.Marshal(rec)
	if err != nil {
		h.logger.Error("trace", "err", err)
		return
	}
	buf = append(buf, '\n')
	h.traceMu.Lock()
	defer h.traceMu.Unlock()
	if _, err := h.trace.Write(buf); err != nil {
		h.logger.Error("trace", "err", err)
	}
}

func pollGeneration(ctx context.Context, st vectorstore.Store, notify func() error, every time.Duration) {
	last := int64(-1)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			n, err := st.Generation(ctx)
			if err != nil {
				continue
			}
			if last >= 0 && n != last {
				_ = notify()
			}
			last = n
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func toDTO(chunks []parser.Chunk) []chunkDTO {
	out := make([]chunkDTO, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, chunkDTO{
			FilePath: chunk.FilePath,
			Type:     chunk.Type,
			Content:  chunk.Content,
			Metadata: chunk.Metadata,
			Outbound: chunk.Outbound,
		})
	}
	return out
}

func clampLimit(n int) int {
	if n < 1 {
		return defaultSemLimit
	}
	if n > maxSemLimit {
		return maxSemLimit
	}
	return n
}

func logLevel(name string) slog.Level {
	switch strings.ToLower(name) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
