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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/ignore"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/query"
	"github.com/axitdev/onfly/internal/rules"
	"github.com/axitdev/onfly/internal/tokens"
	"github.com/axitdev/onfly/internal/usage"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/watch"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is set at build time with -ldflags -X.
var Version = "dev"

const (
	maxRawBytes  = 1 << 20
	keywordLimit = 20
)

var componentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type chunkDTO struct {
	Rules    string            `json:"rules,omitempty"`
	FilePath string            `json:"file_path"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Outbound []string          `json:"outbound,omitempty"`
	// Alternatives lists other definitions of the requested component name.
	Alternatives []altDTO `json:"alternatives,omitempty"`
	// AlternativesOmitted counts alternatives dropped by the cap.
	AlternativesOmitted int `json:"alternatives_omitted,omitempty"`
}

const maxAlternatives = 20

type altDTO struct {
	FilePath  string `json:"file_path"`
	Receiver  string `json:"receiver,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
}

type hitDTO struct {
	FilePath  string `json:"file_path"`
	Symbol    string `json:"symbol"`
	Receiver  string `json:"receiver,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Lines     string `json:"lines"`
	// Brief marks a hit whose Lines holds only the first line (the signature);
	// get_component_ast returns the body.
	Brief bool `json:"brief,omitempty"`
	// Stale marks a hit whose file changed on disk since it was indexed; its
	// line numbers may be off, so read the file before relying on them.
	Stale bool `json:"stale,omitempty"`
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
	Query        string `json:"query" jsonschema:"what the code does, in words"`
	Limit        int    `json:"limit,omitempty" jsonschema:"1 to 50"`
	Workspace    string `json:"workspace,omitempty" jsonschema:"workspace name, or * for every workspace"`
	ChangedSince string `json:"changed_since,omitempty" jsonschema:"git ref; only files changed since it"`
	Type         string `json:"type,omitempty" jsonschema:"one chunk type: function, method, struct, section"`
	Path         string `json:"path,omitempty" jsonschema:"file glob, e.g. internal/**"`
	Detail       string `json:"detail,omitempty" jsonschema:"auto (top 3 with code), full or brief (signatures)"`
}

type keywordIn struct {
	Keyword   string `json:"keyword" jsonschema:"case-insensitive substring"`
	Workspace string `json:"workspace,omitempty" jsonschema:"workspace name, or * for every workspace"`
}

type nameIn struct {
	Name string `json:"name" jsonschema:"component name from chunk metadata"`
}

type componentIn struct {
	Name     string `json:"name" jsonschema:"component name from chunk metadata"`
	Receiver string `json:"receiver,omitempty" jsonschema:"optional receiver or class to pick one of several same-name definitions"`
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
	filter    *ignore.Filter
	logger    *slog.Logger
	searcher  *query.Searcher
	trace     io.Writer
	usage     *usage.Log
	traceMu   sync.Mutex
}

func NewServer(cfg config.Config, store vectorstore.Store, emb embedder.Embedder, logger *slog.Logger) (*sdkmcp.Server, func(), error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	maxTokens := cfg.Server.MaxTokens
	if maxTokens <= 0 {
		maxTokens = config.DefaultMaxTokens
	}
	h := &tools{
		root: cfg.ProjectRoot, store: store, emb: emb,
		workspace: cfg.Workspace, maxTokens: maxTokens, logger: logger,
		engine: rules.Load(cfg),
	}
	rr, err := embedder.NewReranker(cfg.Reranker)
	if err != nil {
		logger.Warn("reranker disabled", "err", err)
		rr = nil
	}
	h.searcher, err = query.NewSearcher(cfg, store, emb, rr, logger)
	if err != nil {
		return nil, nil, err
	}
	h.usage = usage.New(cfg.Server.UsageFile)
	if filter, err := ignore.LoadFilter(cfg.ProjectRoot, cfg.Indexer.Exclude); err != nil {
		logger.Warn("gitignore", "err", err)
	} else {
		h.filter = filter
	}
	if err := h.engine.ReadError(); err != nil {
		logger.Warn("rules", "err", err)
	}
	for _, msg := range h.engine.Report(maxTokens).Syntax {
		logger.Warn("rules", "err", msg)
	}
	closer := func() {}
	if cfg.Server.TraceFile != "" {
		f, err := os.OpenFile(cfg.Server.TraceFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, nil, err
		}
		h.trace = f
		closer = func() { f.Close() }
	}
	instructions := "Search the indexed project. stdout is JSON-RPC only.\n\n" + Guidance
	if text := h.engine.Inject(rules.Query{}, maxTokens); text != "" {
		instructions = text + "\n\n" + instructions
	}
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "onfly", Version: Version}, &sdkmcp.ServerOptions{
		Logger:             logger,
		Instructions:       instructions,
		SubscribeHandler:   func(context.Context, *sdkmcp.SubscribeRequest) error { return nil },
		UnsubscribeHandler: func(context.Context, *sdkmcp.UnsubscribeRequest) error { return nil },
	})
	addTool(srv, &sdkmcp.Tool{Name: "search", Description: "Find code by what it does when you do not know the identifier. Returns symbols, summaries and code; hits past the third are signatures."}, h.search)
	addTool(srv, &sdkmcp.Tool{Name: "find", Description: "Exact lookup. kind text: chunks containing a string or identifier, like Grep over the index. kind symbol: definitions by name; * and ? are wildcards."}, h.find)
	addTool(srv, &sdkmcp.Tool{Name: "graph", Description: "Follow calls from a symbol: callers (what breaks if it changes), callees (what it uses) or references (where its name appears)."}, h.graph)
	addTool(srv, &sdkmcp.Tool{Name: "get_component_ast", Description: "Return the full code of one symbol by name. Use after search or find instead of reading the whole file."}, h.component)
	addTool(srv, &sdkmcp.Tool{Name: "get_file_outline", Description: "List the symbols of one indexed file with their line ranges, in file order."}, h.fileOutline)
	addTool(srv, &sdkmcp.Tool{Name: "get_tests", Description: "Find test files that mention a symbol, with the enclosing test and line. Use before changing a function to see what exercises it."}, h.getTests)
	addTool(srv, &sdkmcp.Tool{Name: "get_history", Description: "Recent commits that touched a symbol's lines: who changed it, when and why. Use to judge how stable or risky a function is."}, h.getHistory)
	h.addPrompts(srv)
	if cfg.Server.FileTools {
		addTool(srv, &sdkmcp.Tool{Name: "read_file_raw", Description: "Read a project file, up to 1MiB."}, h.readFile)
		addTool(srv, &sdkmcp.Tool{Name: "list_directory", Description: "List one directory level inside the project."}, h.listDir)
	}
	addTool(srv, &sdkmcp.Tool{Name: "get_project_rules", Description: "Return the project rules that apply to every request."}, h.projectRules)
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
	lf, err := os.OpenFile(cfg.Server.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
	if err := vectorstore.InitAll(ctx, store, cfg); err != nil {
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
	if cfg.Server.Watch {
		stopWatch := startWatch(ctx, cfg, logger, watch.Run)
		defer stopWatch()
	}
	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()
	go pollGeneration(pollCtx, store, func() error {
		return srv.ResourceUpdated(pollCtx, &sdkmcp.ResourceUpdatedNotificationParams{URI: "onfly://index"})
	}, time.Second)
	return srv.Run(ctx, &sdkmcp.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}})
}

func (h *tools) component(ctx context.Context, _ *sdkmcp.CallToolRequest, in componentIn) (*sdkmcp.CallToolResult, chunkDTO, error) {
	start := time.Now()
	comps, err := h.store.GetComponents(ctx, in.Name)
	if errors.Is(err, vectorstore.ErrNotFound) {
		err = fmt.Errorf("component %q not found", in.Name)
	}
	if err != nil {
		h.record("get_component_ast", in, start, nil, 0)
		return nil, chunkDTO{}, err
	}
	pick := 0
	if in.Receiver != "" {
		pick = -1
		for i, c := range comps {
			if c.Metadata["receiver"] == in.Receiver {
				pick = i
				break
			}
		}
		if pick < 0 {
			h.record("get_component_ast", in, start, nil, 0)
			return nil, chunkDTO{}, fmt.Errorf("component %q has no receiver %q", in.Name, in.Receiver)
		}
	}
	chunk := comps[pick]
	dto := toDTO([]parser.Chunk{chunk})[0]
	for i, c := range comps {
		// With an explicit receiver, only same-receiver definitions in other files are ambiguous.
		if i == pick || in.Receiver != "" && c.Metadata["receiver"] != in.Receiver {
			continue
		}
		if len(dto.Alternatives) >= maxAlternatives {
			dto.AlternativesOmitted++
			continue
		}
		dto.Alternatives = append(dto.Alternatives, altDTO{
			FilePath: c.FilePath, Receiver: c.Metadata["receiver"], StartLine: lineNum(c.Metadata["start_line"]),
		})
	}
	text := h.rulesText(chunkQuery([]parser.Chunk{chunk}))
	h.record("get_component_ast", in, start, dto, 1)
	dto.Rules = text
	return contentWithRules(text, dto), dto, nil
}

func contentWithRules(text string, out any) *sdkmcp.CallToolResult {
	if text == "" {
		return nil
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) == 0 || raw[0] != '{' {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}}
	}
	// The rules are the text above the JSON; leave them out of it so they are
	// not sent twice.
	var box map[string]json.RawMessage
	if json.Unmarshal(raw, &box) == nil {
		if _, ok := box["rules"]; ok {
			delete(box, "rules")
			if slim, err := json.Marshal(box); err == nil {
				raw = slim
			}
		}
	}
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text + "\n" + string(raw)}}}
}

func (h *tools) scope(arg string) string { return query.Scope(h.workspace, arg) }

// trackUsage feeds the usage log: a search remembers its hits, and a later
// open of one of them is recorded with the query that found it.
func (h *tools) trackUsage(tool string, args, result any) {
	if h.usage == nil {
		return
	}
	switch in := args.(type) {
	case semanticIn:
		hits, _ := result.([]hitDTO)
		var seen []usage.Hit
		for _, x := range hits {
			if x.FilePath != "" {
				seen = append(seen, usage.Hit{File: x.FilePath, Symbol: x.Symbol})
			}
		}
		h.usage.Search(in.Query, seen)
	case componentIn:
		_ = h.usage.Open("", in.Name)
	case graphIn:
		_ = h.usage.Open(in.FilePath, in.Name)
	case pathIn:
		_ = h.usage.Open(in.Path, "")
	case outlineIn:
		_ = h.usage.Open(in.FilePath, "")
	}
}

func (h *tools) record(tool string, args any, start time.Time, result any, chunks int) {
	h.trackUsage(tool, args, result)
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

func clampLimit(n int) int { return query.ClampLimit(n) }

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

// startWatch runs the index watcher beside the MCP server. A watcher error is
// logged and does not end the session. stop cancels it and waits.
func startWatch(ctx context.Context, cfg config.Config, logger *slog.Logger, run func(context.Context, config.Config, *slog.Logger) error) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := run(ctx, cfg, logger); err != nil && ctx.Err() == nil {
			logger.Error("watch stopped", "err", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
