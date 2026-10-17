// Package audit reports where an assistant's tokens go on a project: what the
// Onfly tools saved, which large files were read whole instead, and what to do
// about it. Its inputs are the telemetry Onfly keeps, the sizes of the indexed
// files, and optionally the assistant's own session logs, because Onfly cannot
// see a Read or Grep it did not serve.
package audit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/axitdev/onfly/internal/telemetry"
)

const (
	// A file at least this many tokens long is worth a skeleton instead of a full read.
	largeFileTokens = 500
	maxListed       = 8
)

// Inputs are what the audit reads.
type Inputs struct {
	Telemetry  *telemetry.Log
	Transcript *Transcript
	Files      []string         // indexed paths, relative to the project root
	SizeOf     func(string) int // bytes of a file
	Since      time.Time
}

// ToolLine is the telemetry of one tool.
type ToolLine struct {
	Tool        string `json:"tool"`
	Requests    int    `json:"requests"`
	TokensSaved int    `json:"tokens_saved"`
	// Reduction is the share of the raw size that did not reach the model.
	Reduction float64 `json:"reduction"`
}

// FileLine is one large file and how it was read.
type FileLine struct {
	Path      string `json:"path"`
	Tokens    int    `json:"tokens"`
	FullReads int    `json:"full_reads"`
	XrayCalls int    `json:"xray_calls"`
}

// Report is the audit.
type Report struct {
	Tools      []ToolLine `json:"tools"`
	LargeFiles []FileLine `json:"large_files"`
	FullReads  int        `json:"full_reads_of_large_files"`
	ReadTokens int        `json:"tokens_in_those_reads"`
	Transcript *Summary   `json:"transcript,omitempty"`
	Tips       []string   `json:"tips"`
}

// Summary is the part of the transcript that matters here.
type Summary struct {
	Sessions   int `json:"sessions"`
	OnflyCalls int `json:"onfly_calls"`
	Searches   int `json:"grep_and_glob_calls"`
	Reads      int `json:"read_calls"`
}

// Build computes the report.
func Build(ctx context.Context, in Inputs) (Report, error) {
	var rep Report
	targets, err := in.Telemetry.ByTarget(ctx, in.Since)
	if err != nil {
		return rep, err
	}
	byTool := map[string]*ToolLine{}
	raw := map[string]int{}
	comp := map[string]int{}
	xray := map[string]int{}
	var noSaving []string
	for _, t := range targets {
		l := byTool[t.Tool]
		if l == nil {
			l = &ToolLine{Tool: t.Tool}
			byTool[t.Tool] = l
		}
		l.Requests += t.Requests
		l.TokensSaved += t.Saved()
		raw[t.Tool] += t.RawBytes
		comp[t.Tool] += t.CompressedBytes
		if t.Tool == "onfly_xray" {
			xray[t.Target] += t.Requests
			if t.Saved() == 0 && !strings.HasSuffix(t.Target, "/") {
				noSaving = append(noSaving, t.Target)
			}
		}
	}
	for tool, l := range byTool {
		if raw[tool] > comp[tool] {
			l.Reduction = float64(raw[tool]-comp[tool]) / float64(raw[tool])
		}
		rep.Tools = append(rep.Tools, *l)
	}
	sort.Slice(rep.Tools, func(i, j int) bool { return rep.Tools[i].TokensSaved > rep.Tools[j].TokensSaved })

	var reads map[string]int
	if in.Transcript != nil {
		reads = in.Transcript.FullReads
		rep.Transcript = &Summary{
			Sessions: in.Transcript.Sessions, OnflyCalls: in.Transcript.OnflyCalls(),
			Searches: in.Transcript.SearchCalls(), Reads: in.Transcript.Tools["Read"],
		}
	}
	for _, f := range in.Files {
		tokens := in.SizeOf(f) / 4
		if tokens < largeFileTokens {
			continue
		}
		rep.LargeFiles = append(rep.LargeFiles, FileLine{Path: f, Tokens: tokens, FullReads: reads[f], XrayCalls: xray[f]})
		rep.FullReads += reads[f]
		rep.ReadTokens += reads[f] * tokens
	}
	sort.Slice(rep.LargeFiles, func(i, j int) bool {
		a, b := rep.LargeFiles[i], rep.LargeFiles[j]
		if a.FullReads*a.Tokens != b.FullReads*b.Tokens {
			return a.FullReads*a.Tokens > b.FullReads*b.Tokens
		}
		return a.Tokens > b.Tokens
	})
	if len(rep.LargeFiles) > maxListed {
		rep.LargeFiles = rep.LargeFiles[:maxListed]
	}
	rep.Tips = tips(rep, noSaving)
	return rep, nil
}

func tips(rep Report, noSaving []string) []string {
	var out []string
	if len(rep.Tools) == 0 && rep.Transcript == nil {
		return []string{"No data yet: use the tools in a session (onfly setup claude registers them), or pass --transcripts to read an assistant's session logs."}
	}
	if rep.Transcript != nil && rep.Transcript.Sessions > 0 && rep.Transcript.OnflyCalls == 0 {
		out = append(out, "The assistant never called an Onfly tool in these sessions. Run `onfly setup claude` to register the server and write the routing guide into CLAUDE.md.")
	}
	if rep.FullReads > 0 {
		var worst []string
		for _, f := range rep.LargeFiles {
			if f.FullReads > 0 && len(worst) < 3 {
				worst = append(worst, fmt.Sprintf("%s (%d reads, %d tokens each)", f.Path, f.FullReads, f.Tokens))
			}
		}
		out = append(out, fmt.Sprintf("%d full reads of %d-token-or-longer files, about %d tokens. Call onfly_xray on a large file first; it returns the signatures and who calls them for roughly half the tokens, and a body can be read afterwards by line range. Most read: %s.",
			rep.FullReads, largeFileTokens, rep.ReadTokens, strings.Join(worst, "; ")))
	}
	if t := rep.Transcript; t != nil {
		focus := 0
		for _, l := range rep.Tools {
			if l.Tool == "onfly_focus" {
				focus = l.Requests
			}
		}
		if t.Searches >= 10 && t.Searches > 2*focus {
			out = append(out, fmt.Sprintf("Grep and Glob were called %d times against %d onfly_focus calls. For a question about a flow, one onfly_focus returns the matching code and what it uses.", t.Searches, focus))
		}
	}
	if len(noSaving) > 0 {
		sort.Strings(noSaving)
		if len(noSaving) > 5 {
			noSaving = noSaving[:5]
		}
		out = append(out, "onfly_xray saved nothing on "+strings.Join(noSaving, ", ")+": small files and interfaces are about as cheap to read whole.")
	}
	if len(out) == 0 {
		out = append(out, "Nothing stands out.")
	}
	return out
}

// Text renders the report for a terminal.
func (r Report) Text() string {
	var b strings.Builder
	b.WriteString("onfly tools\n")
	if len(r.Tools) == 0 {
		b.WriteString("  no calls recorded\n")
	}
	for _, t := range r.Tools {
		fmt.Fprintf(&b, "  %-12s %5d calls, %7d tokens saved (%.0f%% smaller than reading)\n", t.Tool, t.Requests, t.TokensSaved, t.Reduction*100)
	}
	if t := r.Transcript; t != nil {
		fmt.Fprintf(&b, "\nassistant sessions: %d, %d onfly calls, %d Read, %d Grep/Glob\n", t.Sessions, t.OnflyCalls, t.Reads, t.Searches)
	}
	if len(r.LargeFiles) > 0 {
		fmt.Fprintf(&b, "\nlarge files (%d tokens or more)\n", largeFileTokens)
		for _, f := range r.LargeFiles {
			fmt.Fprintf(&b, "  %-52s %6d tokens, read whole %d, xray %d\n", f.Path, f.Tokens, f.FullReads, f.XrayCalls)
		}
	}
	b.WriteString("\n")
	for _, tip := range r.Tips {
		b.WriteString("tip: " + tip + "\n")
	}
	return b.String()
}
