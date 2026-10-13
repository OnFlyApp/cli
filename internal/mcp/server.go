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
	"github.com/axitdev/onfly/internal/rules"
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
	Rules    string            `json:"rules,omitempty"`
	FilePath string            `json:"file_path"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Outbound []string          `json:"outbound,omitempty"`
}

type hitDTO struct {
	FilePath  string `json:"file_path"`
	Symbol    string `json:"symbol"`
	Receiver  string `json:"receiver,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Lines     string `json:"lines"`
}

type hitList struct {
	Rules string   `json:"rules,omitempty"`
	Hits  []hitDTO `json:"hits"`
}

type chunkList struct {
	Rules  string     `json:"rules,omitempty"`
	Chunks []chunkDTO `json:"chunks"`
}

type dirList struct {
	Rules   string     `json:"rules,omitempty"`
	Entries []dirEntry `json:"entries"`
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
	Rules   string `json:"rules,omitempty"`
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
	store     vectorstore.Store
	emb       embedder.Embedder
	workspace string
	maxTokens int
	engine    *rules.Engine
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
		root: cfg.ProjectRoot, store: store, emb: emb,
		workspace: cfg.Workspace, maxTokens: maxTokens, logger: logger,
		engine: rules.Load(cfg),
	}
	if err := h.engine.ReadError(); err != nil {
		logger.Warn("rules", "err", err)
	}
	for _, msg := range h.engine.Report(maxTokens).Syntax {
		logger.Warn("rules", "err", msg)
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
	instructions := "Search the indexed project. stdout is JSON-RPC only."
	if text := h.engine.Inject(rules.Query{}, maxTokens); text != "" {
		instructions = text + "\n\n" + instructions
	}
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "onfly", Version: "0.4.0"}, &sdkmcp.ServerOptions{
		Logger:             logger,
		Instructions:       instructions,
		SubscribeHandler:   func(context.Context, *sdkmcp.SubscribeRequest) error { return nil },
		UnsubscribeHandler: func(context.Context, *sdkmcp.UnsubscribeRequest) error { return nil },
	})
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "search", Description: "Find code by what it does. Returns the path, the symbol, and the lines to edit."}, h.search)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "search_keyword", Description: "Find chunks whose text contains an identifier you already know. For a description of behavior, call search."}, h.searchKeyword)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_component_ast", Description: "Return the chunk whose metadata name matches. Split parts of that symbol are joined."}, h.component)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_references", Description: "Return chunks whose outbound references include the name."}, h.references)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "read_file_raw", Description: "Read a project file, up to 1MiB."}, h.readFile)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "list_directory", Description: "List one directory level inside the project."}, h.listDir)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "get_project_rules", Description: "Return the project rules that apply to every request."}, h.projectRules)
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

func (h *tools) search(ctx context.Context, req *sdkmcp.CallToolRequest, in semanticIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	if strings.TrimSpace(in.Query) == "" {
		h.record("search", in, start, nil, 0)
		return nil, hitList{}, fmt.Errorf("query is required")
	}
	if in.Workspace == "" {
		ws, asked, err := workspaceChoice(req)
		if err != nil {
			h.record("search", in, start, nil, 0)
			return nil, hitList{}, err
		}
		if asked {
			names, err := h.store.WorkspacesForName(ctx, in.Query)
			if err != nil {
				h.record("search", in, start, nil, 0)
				return nil, hitList{}, err
			}
			if !containsString(names, ws) {
				h.record("search", in, start, nil, 0)
				return nil, hitList{}, fmt.Errorf("workspace %q is not in the index", ws)
			}
			return h.finishSearch(ctx, in, ws, start)
		}
		if componentName.MatchString(in.Query) {
			names, err := h.store.WorkspacesForName(ctx, in.Query)
			if err != nil {
				h.record("search", in, start, nil, 0)
				return nil, hitList{}, err
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
				}, hitList{}, nil
			}
		}
	}
	return h.finishSearch(ctx, in, h.scope(in.Workspace), start)
}

func (h *tools) finishSearch(ctx context.Context, in semanticIn, workspace string, start time.Time) (*sdkmcp.CallToolResult, hitList, error) {
	vec, err := h.emb.Embed(ctx, in.Query)
	if err != nil {
		h.record("search", in, start, nil, 0)
		return nil, hitList{}, err
	}
	pool := candidatePool(in.Limit)
	semantic, err := h.store.SearchSemantic(ctx, vec, pool, workspace)
	if err != nil {
		h.record("search", in, start, nil, 0)
		return nil, hitList{}, err
	}
	keyword, err := h.store.SearchKeyword(ctx, in.Query, pool, workspace)
	if err != nil {
		h.logger.Warn("search keyword", "err", err)
		keyword = nil
	}
	fused := fuseChunks(semantic, keyword)
	limit := clampLimit(in.Limit)
	if len(fused) > limit {
		fused = fused[:limit]
	}
	budget, text := h.fitBudget(chunkQuery(fused))
	hits, err := fitHits(fused, budget)
	logged := in
	logged.Workspace = workspace
	h.record("search", logged, start, hits, len(hits))
	out := hitList{Rules: text, Hits: hits}
	if out.Hits == nil {
		out.Hits = []hitDTO{}
	}
	return contentWithRules(text, out), out, err
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

func (h *tools) searchKeyword(ctx context.Context, _ *sdkmcp.CallToolRequest, in keywordIn) (*sdkmcp.CallToolResult, hitList, error) {
	start := time.Now()
	chunks, err := h.store.SearchKeyword(ctx, in.Keyword, keywordLimit, h.scope(in.Workspace))
	if err != nil {
		h.record("search_keyword", in, start, nil, 0)
		return nil, hitList{}, err
	}
	budget, text := h.fitBudget(chunkQuery(chunks))
	hits, err := fitHits(chunks, budget)
	h.record("search_keyword", in, start, hits, len(hits))
	out := hitList{Rules: text, Hits: hits}
	if out.Hits == nil {
		out.Hits = []hitDTO{}
	}
	return contentWithRules(text, out), out, err
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
	text := h.rulesText(chunkQuery([]parser.Chunk{*chunk}))
	h.record("get_component_ast", in, start, dto, 1)
	dto.Rules = text
	return contentWithRules(text, dto), dto, nil
}

func (h *tools) references(ctx context.Context, _ *sdkmcp.CallToolRequest, in nameIn) (*sdkmcp.CallToolResult, chunkList, error) {
	start := time.Now()
	chunks, err := h.store.GetReferences(ctx, in.Name)
	if err != nil {
		h.record("get_references", in, start, nil, 0)
		return nil, chunkList{}, err
	}
	budget, text := h.fitBudget(chunkQuery(chunks))
	dtos, err := fit(chunks, budget)
	h.record("get_references", in, start, dtos, len(dtos))
	if dtos == nil {
		dtos = []chunkDTO{}
	}
	out := chunkList{Rules: text, Chunks: dtos}
	return contentWithRules(text, out), out, err
}

func (h *tools) readFile(_ context.Context, _ *sdkmcp.CallToolRequest, in pathIn) (*sdkmcp.CallToolResult, fileOut, error) {
	start := time.Now()
	out, err := h.readFileBody(in)
	if err != nil {
		h.record("read_file_raw", in, start, nil, 0)
		return nil, fileOut{}, err
	}
	text := h.rulesText(rules.Query{Targets: []rules.Target{{Path: requestPath(h.root, in.Path)}}})
	h.record("read_file_raw", in, start, out, 0)
	out.Rules = text
	return contentWithRules(text, out), out, nil
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

func (h *tools) listDir(_ context.Context, _ *sdkmcp.CallToolRequest, in pathIn) (*sdkmcp.CallToolResult, dirList, error) {
	start := time.Now()
	entries, err := h.listDirBody(in)
	if err != nil {
		h.record("list_directory", in, start, nil, 0)
		return nil, dirList{}, err
	}
	path := in.Path
	if path == "" {
		path = "."
	}
	text := h.rulesText(rules.Query{Targets: []rules.Target{{Path: requestPath(h.root, path)}}})
	h.record("list_directory", in, start, entries, 0)
	if entries == nil {
		entries = []dirEntry{}
	}
	out := dirList{Rules: text, Entries: entries}
	return contentWithRules(text, out), out, nil
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
	if h.engine == nil {
		return rulesOut{}, nil
	}
	if err := h.engine.ReadError(); err != nil {
		return rulesOut{}, err
	}
	return rulesOut{Rules: h.engine.Inject(rules.Query{}, h.maxTokens)}, nil
}

func (h *tools) rulesText(q rules.Query) string {
	if h.engine == nil {
		return ""
	}
	return h.engine.Inject(q, h.maxTokens)
}

func (h *tools) fitBudget(q rules.Query) (int, string) {
	text := h.rulesText(q)
	if text == "" {
		return h.maxTokens, ""
	}
	n, err := tokens.Count(text)
	if err != nil || n <= 0 {
		return h.maxTokens, text
	}
	if n >= h.maxTokens {
		return 1, text
	}
	return h.maxTokens - n, text
}

func chunkQuery(chunks []parser.Chunk) rules.Query {
	q := rules.Query{Targets: make([]rules.Target, 0, len(chunks))}
	for _, chunk := range chunks {
		q.Targets = append(q.Targets, rules.Target{Path: chunk.FilePath, Type: chunk.Type})
	}
	return q
}

func requestPath(root, p string) string {
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(root, p); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

func contentWithRules(text string, out any) *sdkmcp.CallToolResult {
	if text == "" {
		return nil
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) == 0 || raw[0] != '{' {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}}
	}
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text + "\n" + string(raw)}}}
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

func truncationText(omitted int) string {
	return fmt.Sprintf("// [Truncated: %d matches omitted due to context limits.]", omitted)
}

func truncation(omitted int) parser.Chunk {
	return parser.Chunk{Type: "diagnostic", Content: truncationText(omitted)}
}

func diagnosticHit(omitted int) hitDTO {
	return hitDTO{Lines: truncationText(omitted)}
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

func candidatePool(limit int) int {
	n := clampLimit(limit)
	if n < keywordLimit {
		return keywordLimit
	}
	return n
}

func fitHits(chunks []parser.Chunk, maxTokens int) ([]hitDTO, error) {
	var kept []parser.Chunk
	for _, ch := range chunks {
		trial := append(append([]parser.Chunk{}, kept...), ch)
		omitted := len(chunks) - len(trial)
		payload := toHits(trial)
		if omitted > 0 {
			payload = append(payload, diagnosticHit(omitted))
		}
		n, err := tokens.Count(mustJSON(payload))
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
		return toHits(kept), nil
	}
	only := []hitDTO{diagnosticHit(len(chunks))}
	if len(kept) == 0 {
		return only, nil
	}
	return append(toHits(kept), diagnosticHit(omitted)), nil
}

func toHits(chunks []parser.Chunk) []hitDTO {
	out := make([]hitDTO, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, toHit(chunk))
	}
	return out
}

func toHit(chunk parser.Chunk) hitDTO {
	hit := hitDTO{FilePath: chunk.FilePath, Lines: chunk.Content}
	if chunk.Metadata == nil {
		return hit
	}
	hit.Symbol = chunk.Metadata["name"]
	if r := chunk.Metadata["receiver"]; r != "" {
		hit.Receiver = r
	}
	hit.StartLine = lineNum(chunk.Metadata["start_line"])
	hit.EndLine = lineNum(chunk.Metadata["end_line"])
	return hit
}

func lineNum(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func fuseChunks(lists ...[]parser.Chunk) []parser.Chunk {
	const k = 60
	type acc struct {
		chunk parser.Chunk
		score float64
	}
	order := []string{}
	by := map[string]*acc{}
	for _, list := range lists {
		seen := map[string]bool{}
		rank := 0
		for _, ch := range list {
			key := chunkKey(ch)
			if seen[key] {
				continue
			}
			seen[key] = true
			rank++
			item := by[key]
			if item == nil {
				item = &acc{chunk: ch}
				by[key] = item
				order = append(order, key)
			}
			item.score += 1 / float64(k+rank)
		}
	}
	out := make([]parser.Chunk, 0, len(order))
	for _, key := range order {
		out = append(out, by[key].chunk)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si := by[chunkKey(out[i])].score
		sj := by[chunkKey(out[j])].score
		if si != sj {
			return si > sj
		}
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		return out[i].Content < out[j].Content
	})
	return out
}

func chunkKey(c parser.Chunk) string {
	meta, err := json.Marshal(c.Metadata)
	if err != nil {
		meta = []byte("null")
	}
	out, err := json.Marshal(c.Outbound)
	if err != nil {
		out = []byte("null")
	}
	return c.FilePath + "\x00" + c.Type + "\x00" + c.Content + "\x00" + string(meta) + "\x00" + string(out)
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
