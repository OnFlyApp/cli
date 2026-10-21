package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/axitdev/onfly/internal/config"
	"github.com/axitdev/onfly/internal/embedder"
	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/rules"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check config, database, embedder, parsers, and rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stdout, "config: %s\n", strings.TrimPrefix(err.Error(), "config: "))
			return fmt.Errorf("doctor: failed")
		}
		return Doctor(cmd.Context(), cfg, os.Stdout)
	},
}

func Doctor(ctx context.Context, cfg config.Config, out io.Writer) error {
	fmt.Fprintln(out, "config: ok")
	failed := false
	if err := checkDatabase(ctx, cfg, out); err != nil {
		failed = true
	}
	if err := checkEmbedder(ctx, cfg, out); err != nil {
		failed = true
	}
	if err := checkReranker(cfg, out); err != nil {
		failed = true
	}
	if err := checkPlugins(cfg, out); err != nil {
		failed = true
	}
	if err := checkRules(cfg, out); err != nil {
		failed = true
	}
	checkTuning(cfg, out)
	if failed {
		return fmt.Errorf("doctor: failed")
	}
	return nil
}

func checkDatabase(ctx context.Context, cfg config.Config, out io.Writer) error {
	if cfg.Database.Driver == "sqlite" && cfg.Database.DSN != ":memory:" {
		if _, err := os.Stat(cfg.Database.DSN); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(out, "database: database file missing (run `onfly index` to create it)")
				return err
			}
			fmt.Fprintf(out, "database: %s\n", err)
			return err
		}
	}
	st, err := vectorstore.OpenChecker(ctx, cfg.Database)
	if err != nil {
		fmt.Fprintf(out, "database: %s\n", err)
		return err
	}
	defer st.Close()
	if err := st.CheckMeta(ctx, cfg.Embedder.Dimensions); err != nil {
		fmt.Fprintf(out, "database: %s\n", err)
		return err
	}
	if err := st.Ping(ctx); err != nil {
		fmt.Fprintf(out, "database: %s\n", err)
		return err
	}
	fmt.Fprintln(out, "database: ok")
	if n, err := st.UnnamedChunks(ctx); err == nil && n > 0 {
		fmt.Fprintf(out, "chunks: %d without metadata.name\n", n)
	}
	if s, err := st.Stats(ctx); err == nil {
		for _, h := range append(indexHints(s), contextHints(cfg.Embedder.Model, cfg.Embedder.MaxInputTokens, s)...) {
			fmt.Fprintf(out, "hint: %s\n", h)
		}
	}
	return nil
}

func checkEmbedder(ctx context.Context, cfg config.Config, out io.Writer) error {
	emb, err := embedder.New(cfg.Embedder)
	if err != nil {
		fmt.Fprintf(out, "embedder: %s\n", err)
		return err
	}
	vec, err := emb.Embed(ctx, "onfly")
	if err != nil {
		fmt.Fprintf(out, "embedder: %s\n", err)
		return err
	}
	if len(vec) != cfg.Embedder.Dimensions {
		err = fmt.Errorf("dimensions got %d, want %d", len(vec), cfg.Embedder.Dimensions)
		fmt.Fprintf(out, "embedder: %s\n", err)
		return err
	}
	fmt.Fprintln(out, "embedder: ok")
	return nil
}

func checkReranker(cfg config.Config, out io.Writer) error {
	if cfg.Reranker.Provider == "" {
		return nil
	}
	if _, err := embedder.NewReranker(cfg.Reranker); err != nil {
		fmt.Fprintf(out, "reranker: %s\n", err)
		return err
	}
	fmt.Fprintln(out, "reranker: ok")
	return nil
}

func checkPlugins(cfg config.Config, out io.Writer) error {
	var parts []string
	var problems []string
	for _, p := range cfg.Indexer.Parsers {
		switch p.Type {
		case "wasm":
			info, err := os.Stat(p.Path)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			if !info.Mode().IsRegular() {
				problems = append(problems, fmt.Sprintf("%s is not a regular file", p.Path))
				continue
			}
			body, err := os.ReadFile(p.Path)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			if !bytes.HasPrefix(body, []byte("\x00asm")) {
				problems = append(problems, fmt.Sprintf("%s is not a WebAssembly module", p.Path))
				continue
			}
			sum := sha256.Sum256(body)
			parts = append(parts, fmt.Sprintf("%s=%x", filepath.Base(p.Path), sum))
		case "stdio":
			if os.Getenv(parser.AllowStdioEnv) != "1" {
				problems = append(problems, fmt.Sprintf("stdio parser %q needs %s=1", p.Command, parser.AllowStdioEnv))
			}
			fields := strings.Fields(p.Command)
			if len(fields) == 0 {
				problems = append(problems, "stdio parser command is empty")
				continue
			}
			if _, err := exec.LookPath(fields[0]); err != nil {
				problems = append(problems, err.Error())
			}
			for _, field := range fields[1:] {
				if !strings.ContainsAny(field, `/\`) {
					continue
				}
				path := field
				if !filepath.IsAbs(path) {
					path = filepath.Join(cfg.ProjectRoot, path)
				}
				if _, err := os.Stat(path); err != nil {
					problems = append(problems, err.Error())
				}
			}
		case "text":
			parts = append(parts, "text")
		default:
			problems = append(problems, fmt.Sprintf("parser type %q is not available", p.Type))
		}
	}
	if len(problems) > 0 {
		msg := strings.Join(problems, "; ")
		fmt.Fprintf(out, "plugins: %s\n", msg)
		return fmt.Errorf("%s", msg)
	}
	line := "plugins: ok"
	if len(parts) > 0 {
		line += " " + strings.Join(parts, " ")
	}
	fmt.Fprintln(out, line)
	return nil
}

func checkRules(cfg config.Config, out io.Writer) error {
	eng := rules.Load(cfg)
	if err := eng.ReadError(); err != nil {
		fmt.Fprintf(out, "rules: %s\n", err)
		return err
	}
	maxTokens := cfg.Server.MaxTokens
	if maxTokens <= 0 {
		maxTokens = config.DefaultMaxTokens
	}
	rep := eng.Report(maxTokens)
	if len(rep.Syntax) > 0 {
		msg := strings.Join(rep.Syntax, "; ")
		fmt.Fprintf(out, "rules: %s\n", msg)
		return fmt.Errorf("%s", msg)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(out, "rules: [WARNING] %s\n", w)
	}
	if rep.Warning != "" {
		fmt.Fprintf(out, "rules: [WARNING] %s\n", rep.Warning)
		return nil
	}
	if rep.Rules == 0 {
		fmt.Fprintln(out, "rules: ok (none)")
		return nil
	}
	line := fmt.Sprintf("rules: ok %d rules, %d tokens", rep.Rules, rep.Tokens)
	if rep.Fallback != "" {
		line += " (auto-discovered " + rep.Fallback + ")"
	}
	fmt.Fprintln(out, line)
	return nil
}

// checkTuning names the sections of the configuration that change a default. An
// invalid value never gets here: loading the config rejects it.
func checkTuning(cfg config.Config, out io.Writer) {
	var custom []string
	if cfg.Xray != (config.Xray{}) {
		custom = append(custom, "xray")
	}
	if cfg.Resolver.MaxUnambiguousDefinitions != 0 {
		custom = append(custom, "resolver")
	}
	s := cfg.Search
	if s.FocusSemanticWeight != nil || s.FocusKeywordWeight != nil || s.FileSummaryWeight != nil || s.SummarySymbols != 0 || s.CandidatePool != 0 {
		custom = append(custom, "search")
	}
	if cfg.Embedder.TimeoutSeconds != 0 || cfg.Embedder.BatchSize != 0 || cfg.Embedder.QueryPrefix != "" {
		custom = append(custom, "embedder")
	}
	for _, p := range cfg.Indexer.Parsers {
		if len(p.IgnoreDirectives)+len(p.SelfQualifiers)+len(p.Generated)+len(p.GeneratedHeaders) > 0 {
			custom = append(custom, "parser rules "+strings.Join(p.Extensions, ","))
		}
	}
	if len(custom) == 0 {
		fmt.Fprintln(out, "tuning: ok (defaults)")
		return
	}
	fmt.Fprintf(out, "tuning: ok (custom: %s)\n", strings.Join(custom, "; "))
}
