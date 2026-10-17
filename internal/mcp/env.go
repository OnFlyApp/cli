package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/envscan"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxEnvRows = 60

type envIn struct {
	Filter string `json:"filter,omitempty" jsonschema:"keep variables whose name contains this text"`
}

func (h *tools) env(ctx context.Context, req *sdkmcp.CallToolRequest, in envIn) (*sdkmcp.CallToolResult, struct{}, error) {
	start := time.Now()
	res, out, err := h.runEnv(ctx, req, in)
	h.record("onfly_env", in, start, resultText(res), 0)
	return res, out, err
}

func (h *tools) runEnv(ctx context.Context, _ *sdkmcp.CallToolRequest, in envIn) (*sdkmcp.CallToolResult, struct{}, error) {
	if err := h.warm(ctx, ""); err != nil {
		return nil, struct{}{}, err
	}
	rep, err := envscan.Collect(ctx, h.root, h.store)
	if err != nil {
		return nil, struct{}{}, err
	}
	body, raw := RenderEnv(rep, in.Filter, h.fileSize)
	return h.finishMacro(ctx, "onfly_env", "env", raw, `target="env"`, body, "", 0), struct{}{}, nil
}

// RenderEnv is the text of an environment report, and the size of the files a
// reader would otherwise open to learn the same (sizeOf gives a file's bytes).
func RenderEnv(rep envscan.Report, filter string, sizeOf func(string) int) (string, int) {
	filter = strings.ToLower(strings.TrimSpace(filter))
	keep := func(name string) bool { return filter == "" || strings.Contains(strings.ToLower(name), filter) }
	files := map[string]bool{}
	var b strings.Builder
	if len(rep.Files) == 0 {
		b.WriteString("<env_files>none found (.env, .env.example, ...); nothing is reported as missing</env_files>\n")
	} else {
		var names []string
		for n, c := range rep.Files {
			names = append(names, fmt.Sprintf("%s (%d keys)", n, c))
			files[n] = true
		}
		sort.Strings(names)
		b.WriteString("<env_files>" + strings.Join(names, ", ") + "</env_files>\n")
	}
	section := func(tag string, vars []envscan.Var, detail func(envscan.Var) string) {
		var rows []string
		total := 0
		for _, v := range vars {
			if !keep(v.Name) {
				continue
			}
			total++
			if len(rows) < maxEnvRows {
				rows = append(rows, "- "+v.Name+detail(v))
			}
		}
		b.WriteString("<" + tag + ">\n")
		for _, r := range rows {
			b.WriteString(r + "\n")
		}
		if total > len(rows) {
			fmt.Fprintf(&b, "// ... [%d more] ...\n", total-len(rows))
		}
		b.WriteString("</" + tag + ">\n")
	}
	refs := func(v envscan.Var) string {
		if len(v.Used) == 0 {
			return ""
		}
		var parts []string
		for i, r := range v.Used {
			files[r.File] = true
			if i < 3 {
				parts = append(parts, fmt.Sprintf("%s:%d", r.File, r.Line))
			}
		}
		more := ""
		if len(v.Used) > 3 {
			more = fmt.Sprintf(" (+%d)", len(v.Used)-3)
		}
		return "  used: " + strings.Join(parts, ", ") + more
	}
	section("variables", rep.Vars, func(v envscan.Var) string {
		d := ""
		if len(v.Defined) > 0 {
			d = "  defined: " + strings.Join(v.Defined, ",")
		}
		return d + refs(v)
	})
	section("missing_from_env_files", rep.Missing, refs)
	section("unused", rep.Unused, func(v envscan.Var) string { return "  defined: " + strings.Join(v.Defined, ",") })
	raw := 0
	for f := range files {
		raw += sizeOf(f)
	}
	return b.String(), raw
}
