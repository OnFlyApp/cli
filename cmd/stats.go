package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/telemetry"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/spf13/cobra"
)

var statsFormat string

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Print index size and chunk breakdown",
	RunE: func(cmd *cobra.Command, args []string) error {
		if statsFormat != "text" && statsFormat != "json" {
			return fmt.Errorf("unknown --format %q: use text or json", statsFormat)
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if statsFormat == "json" {
			return writeTelemetryJSON(cmd.Context(), cmd.OutOrStdout(), cfg.Server.TelemetryPath())
		}
		if cfg.Database.Driver == "sqlite" && cfg.Database.DSN != ":memory:" {
			if _, err := os.Stat(cfg.Database.DSN); err != nil {
				return fmt.Errorf("no index yet at %s. Run: onfly index", cfg.Database.DSN)
			}
		}
		st, err := vectorstore.OpenChecker(cmd.Context(), cfg.Database)
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.CheckMeta(cmd.Context(), cfg.Embedder.Dimensions); err != nil {
			return err
		}
		s, err := st.Stats(cmd.Context())
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "index: %s, %d dimensions\n", dbLabel(cfg.Database), cfg.Embedder.Dimensions)
		fmt.Fprintf(out, "embedder: %s %s\n", cfg.Embedder.Provider, cfg.Embedder.Model)
		formatStats(out, s)
		for _, h := range contextHints(cfg.Embedder.Model, cfg.Embedder.MaxInputTokens, s) {
			fmt.Fprintf(out, "hint: %s\n", h)
		}
		if s.Files == 0 {
			fmt.Fprintln(out, "the index is empty. Run: onfly index")
		}
		return printTelemetry(cmd.Context(), out, cfg.Server.TelemetryPath())
	},
}

func init() {
	statsCmd.Flags().StringVar(&statsFormat, "format", "text", "text or json; json prints only the telemetry totals")
}

const usdPerMillionInputTokens = 3.0

// writeTelemetryJSON prints the telemetry totals for CI. A missing telemetry
// file reads as all zeros so a pipeline can parse the output either way.
func writeTelemetryJSON(ctx context.Context, w io.Writer, path string) error {
	var s telemetry.Summary
	if _, err := os.Stat(path); err == nil {
		tel, err := telemetry.Open(path)
		if err != nil {
			return err
		}
		defer tel.Close()
		if s, err = tel.Summary(ctx); err != nil {
			return err
		}
	}
	saved := s.Saved()
	return json.NewEncoder(w).Encode(map[string]any{
		"requests":       s.Requests,
		"tokens_saved":   saved,
		"ratio":          s.Ratio(),
		"usd_saved":      float64(saved) / 1e6 * usdPerMillionInputTokens,
		"rules_injected": s.Rules,
	})
}

// printTelemetry shows what onfly_xray and onfly_focus kept out of the model's
// context. It stays silent until the server has recorded a call.
func printTelemetry(ctx context.Context, w io.Writer, path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	tel, err := telemetry.Open(path)
	if err != nil {
		return err
	}
	defer tel.Close()
	s, err := tel.Summary(ctx)
	if err != nil || s.Requests == 0 {
		return err
	}
	saved := s.Saved()
	fmt.Fprintf(w, "\ntelemetry (estimated at 4 bytes per token)\n")
	fmt.Fprintf(w, "requests served: %d\n", s.Requests)
	fmt.Fprintf(w, "tokens saved: %d (%.0f%% smaller than reading the files)\n", saved, s.Ratio()*100)
	fmt.Fprintf(w, "usd saved: $%.2f at $%.0f per million input tokens\n", float64(saved)/1e6*usdPerMillionInputTokens, usdPerMillionInputTokens)
	fmt.Fprintf(w, "rules injected: %d\n", s.Rules)
	return nil
}

func formatStats(w io.Writer, s vectorstore.IndexStats) {
	fmt.Fprintf(w, "files: %d\n", s.Files)
	if s.Unnamed > 0 {
		fmt.Fprintf(w, "chunks: %d (%d without metadata.name)\n", s.Chunks, s.Unnamed)
	} else {
		fmt.Fprintf(w, "chunks: %d\n", s.Chunks)
	}
	fmt.Fprintf(w, "types: %s\n", joinCounts(s.ByType))
	if named := withoutEmpty(s.Workspaces); len(named) > 0 {
		fmt.Fprintf(w, "workspaces: %s\n", joinCounts(named))
	}
	if top := topExtensions(s, 5); len(top) > 0 {
		fmt.Fprintf(w, "extensions: %s\n", strings.Join(top, ", "))
	}
	for _, h := range indexHints(s) {
		fmt.Fprintf(w, "hint: %s\n", h)
	}
}

func extShares(s vectorstore.IndexStats) (exts []string, share map[string]float64) {
	total := 0
	for _, e := range s.ByExt {
		total += e.Bytes
	}
	share = map[string]float64{}
	if total == 0 {
		return nil, share
	}
	for ext, e := range s.ByExt {
		exts = append(exts, ext)
		share[ext] = float64(e.Bytes) / float64(total)
	}
	sort.Slice(exts, func(i, j int) bool {
		if share[exts[i]] != share[exts[j]] {
			return share[exts[i]] > share[exts[j]]
		}
		return exts[i] < exts[j]
	})
	return exts, share
}

func topExtensions(s vectorstore.IndexStats, n int) []string {
	exts, share := extShares(s)
	var out []string
	for _, ext := range exts {
		if len(out) == n {
			break
		}
		out = append(out, fmt.Sprintf("%s %.0f%% (%d files)", ext, share[ext]*100, s.ByExt[ext].Files))
	}
	return out
}

var noisyExt = map[string]bool{
	".json": true, ".lock": true, ".svg": true, ".csv": true, ".map": true, ".min.js": true,
	".min.css": true, ".snap": true, ".sum": true, ".xml": true, ".log": true, ".sql": true,
}

var modelContext = map[string]int{
	"nomic-embed-text":       2048,
	"embeddinggemma":         2048,
	"mxbai-embed-large":      512,
	"all-minilm":             256,
	"snowflake-arctic-embed": 512,
}

func contextHints(model string, maxInput int, s vectorstore.IndexStats) []string {
	var out []string
	name := strings.TrimSuffix(strings.SplitN(model, ":", 2)[0], ":latest")
	if ctx, ok := modelContext[name]; ok && (maxInput == 0 || maxInput > ctx) {
		out = append(out, fmt.Sprintf("%s reads about %d tokens; embedder.max_input_tokens is %d, so longer chunks are cut without a warning. Set max_input_tokens: %d and reindex", model, ctx, max(maxInput, 6000), ctx-100))
	}
	if s.LongChunks > 0 {
		out = append(out, fmt.Sprintf("%d chunks are longer than about 2000 tokens (the longest about %d). An embedder with a 2048-token context cuts them; check embedder.max_input_tokens against your model", s.LongChunks, s.MaxChunkBytes/4))
	}
	return out
}

func indexHints(s vectorstore.IndexStats) []string {
	exts, share := extShares(s)
	var out []string
	for _, ext := range exts {
		if noisyExt[ext] && share[ext] >= 0.15 {
			out = append(out, fmt.Sprintf("%s files are %.0f%% of indexed text (%d files); consider adding them to indexer.exclude", ext, share[ext]*100, s.ByExt[ext].Files))
		}
	}
	return out
}

func withoutEmpty(m map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		if k != "" {
			out[k] = v
		}
	}
	return out
}

func joinCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}
