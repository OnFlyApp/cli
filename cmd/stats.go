package cmd

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Print index size and chunk breakdown",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
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
		formatStats(cmd.OutOrStdout(), s)
		return nil
	},
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

// topExtensions lists the n extensions holding the most indexed text.
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

// noisyExt are extensions that are usually data or generated output, not code.
var noisyExt = map[string]bool{
	".json": true, ".lock": true, ".svg": true, ".csv": true, ".map": true, ".min.js": true,
	".min.css": true, ".snap": true, ".sum": true, ".xml": true, ".log": true, ".sql": true,
}

// indexHints names noisy extensions that take a large share of the index.
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
