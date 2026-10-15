package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/axitdev/onfly/internal/ignore"
	"github.com/axitdev/onfly/internal/rules"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

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

func (h *tools) rejectSensitive(full string, isDir bool) error {
	resolvedRoot, err := filepath.EvalSymlinks(h.root)
	if err != nil {
		resolvedRoot = h.root
	}
	rel, err := filepath.Rel(resolvedRoot, full)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if ignore.Sensitive(rel) || h.filter.Skip(rel, isDir) {
		return fmt.Errorf("path is not readable")
	}
	return nil
}

func (h *tools) readFileBody(in pathIn) (fileOut, error) {
	full, err := SafePath(h.root, in.Path)
	if err != nil {
		return fileOut{}, err
	}
	if err := h.rejectSensitive(full, false); err != nil {
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
	if err := h.rejectSensitive(full, true); err != nil {
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
		if h.rejectSensitive(filepath.Join(full, entry.Name()), entry.IsDir()) != nil {
			continue
		}
		out = append(out, dirEntry{Name: entry.Name(), Kind: kind})
	}
	return out, nil
}
