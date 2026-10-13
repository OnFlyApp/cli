package cmd

import (
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
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check config, database, embedder, and parsers",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stdout, "config: %s\n", err)
			return err
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
	if err := checkPlugins(cfg, out); err != nil {
		failed = true
	}
	if failed {
		return fmt.Errorf("doctor: failed")
	}
	return nil
}

func checkDatabase(ctx context.Context, cfg config.Config, out io.Writer) error {
	if cfg.Database.Driver == "sqlite" && cfg.Database.DSN != ":memory:" {
		if _, err := os.Stat(cfg.Database.DSN); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(out, "database: database file missing")
				return err
			}
			fmt.Fprintf(out, "database: %s\n", err)
			return err
		}
	}
	var ping func(context.Context) error
	var closeFn func() error
	switch cfg.Database.Driver {
	case "sqlite":
		st, err := vectorstore.OpenSQLite(cfg.Database.DSN)
		if err != nil {
			fmt.Fprintf(out, "database: %s\n", err)
			return err
		}
		if err := st.CheckMeta(ctx, cfg.Embedder.Dimensions); err != nil {
			st.Close()
			fmt.Fprintf(out, "database: %s\n", err)
			return err
		}
		ping = st.Ping
		closeFn = st.Close
	case "postgres":
		st, err := vectorstore.OpenPostgres(ctx, cfg.Database.DSN)
		if err != nil {
			fmt.Fprintf(out, "database: %s\n", err)
			return err
		}
		if err := st.CheckMeta(ctx, cfg.Embedder.Dimensions); err != nil {
			st.Close()
			fmt.Fprintf(out, "database: %s\n", err)
			return err
		}
		ping = st.Ping
		closeFn = st.Close
	default:
		err := fmt.Errorf("database driver %q is not available", cfg.Database.Driver)
		fmt.Fprintf(out, "database: %s\n", err)
		return err
	}
	defer closeFn()
	if err := ping(ctx); err != nil {
		fmt.Fprintf(out, "database: %s\n", err)
		return err
	}
	fmt.Fprintln(out, "database: ok")
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
			sum := sha256.Sum256(body)
			parts = append(parts, fmt.Sprintf("%s=%x", filepath.Base(p.Path), sum))
		case "stdio":
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
