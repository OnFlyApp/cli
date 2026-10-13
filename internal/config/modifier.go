package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type WasmParser struct {
	Extensions []string
	Path       string
}

func ParserConflicts(configPath string, parser WasmParser) ([]string, error) {
	_, parsers, err := loadDoc(configPath)
	if err != nil {
		return nil, err
	}
	want, err := normalizeList(parser.Extensions)
	if err != nil {
		return nil, err
	}
	return findConflicts(parsers, filepath.ToSlash(parser.Path), want), nil
}

func UpsertWasmParser(configPath string, parser WasmParser) error {
	doc, parsers, err := loadDoc(configPath)
	if err != nil {
		return err
	}
	want, err := normalizeList(parser.Extensions)
	if err != nil {
		return err
	}
	applyParsers(parsers, filepath.ToSlash(parser.Path), want)
	return saveDoc(configPath, doc)
}

func RemoveWasmParser(configPath, relPath string) error {
	doc, parsers, err := loadDoc(configPath)
	if err != nil {
		return err
	}
	rel := filepath.ToSlash(relPath)
	var kept []*yaml.Node
	for _, item := range parsers.Content {
		if item.Kind == yaml.MappingNode && entryType(item) == "wasm" && entryPath(item) == rel {
			continue
		}
		kept = append(kept, item)
	}
	parsers.Content = kept
	return saveDoc(configPath, doc)
}

func WasmParsers(path string) ([]WasmParser, error) {
	_, parsers, err := loadDoc(path)
	if err != nil {
		return nil, err
	}
	var out []WasmParser
	for _, item := range parsers.Content {
		if item.Kind != yaml.MappingNode || entryType(item) != "wasm" {
			continue
		}
		out = append(out, WasmParser{Extensions: entryExts(item), Path: entryPath(item)})
	}
	return out, nil
}

func loadDoc(path string) (*yaml.Node, *yaml.Node, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, nil, err
	}
	root, err := mappingRoot(&doc)
	if err != nil {
		return nil, nil, err
	}
	parsers, err := ensureParsers(root)
	if err != nil {
		return nil, nil, err
	}
	return &doc, parsers, nil
}

func mappingRoot(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config: root must be a mapping")
	}
	return doc.Content[0], nil
}

func ensureParsers(root *yaml.Node) (*yaml.Node, error) {
	indexer := mapGet(root, "indexer")
	if indexer == nil {
		indexer = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "indexer", Tag: "!!str"},
			indexer,
		)
	}
	if indexer.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config: indexer must be a mapping")
	}
	parsers := mapGet(indexer, "parsers")
	if parsers == nil {
		parsers = &yaml.Node{Kind: yaml.SequenceNode}
		indexer.Content = append(indexer.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "parsers", Tag: "!!str"},
			parsers,
		)
	}
	if parsers.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("config: indexer.parsers must be a sequence")
	}
	return parsers, nil
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func findConflicts(parsers *yaml.Node, rel string, want []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range parsers.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if entryType(item) == "wasm" && entryPath(item) == rel {
			continue
		}
		for _, ext := range entryExts(item) {
			if containsStr(want, ext) && !seen[ext] {
				seen[ext] = true
				out = append(out, ext)
			}
		}
	}
	return out
}

func applyParsers(parsers *yaml.Node, rel string, want []string) {
	var kept []*yaml.Node
	replaced := false
	for _, item := range parsers.Content {
		if item.Kind != yaml.MappingNode {
			kept = append(kept, item)
			continue
		}
		if entryType(item) == "wasm" && entryPath(item) == rel {
			setExts(item, want)
			kept = append(kept, item)
			replaced = true
			continue
		}
		exts := entryExts(item)
		next := subtract(exts, want)
		if len(exts) > 0 && len(next) == 0 {
			continue
		}
		if len(next) != len(exts) {
			setExts(item, next)
		}
		kept = append(kept, item)
	}
	if !replaced {
		kept = append(kept, newWasmItem(rel, want))
	}
	parsers.Content = kept
}

func newWasmItem(rel string, exts []string) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "extensions", Tag: "!!str"},
		flowStrings(exts),
		{Kind: yaml.ScalarNode, Value: "type", Tag: "!!str"},
		{Kind: yaml.ScalarNode, Value: "wasm", Tag: "!!str", Style: yaml.DoubleQuotedStyle},
		{Kind: yaml.ScalarNode, Value: "path", Tag: "!!str"},
		{Kind: yaml.ScalarNode, Value: rel, Tag: "!!str", Style: yaml.DoubleQuotedStyle},
	}}
}

func setExts(item *yaml.Node, exts []string) {
	seq := flowStrings(exts)
	for i := 0; i+1 < len(item.Content); i += 2 {
		if item.Content[i].Value == "extensions" {
			item.Content[i+1] = seq
			return
		}
	}
	item.Content = append([]*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "extensions", Tag: "!!str"},
		seq,
	}, item.Content...)
}

func flowStrings(vals []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, v := range vals {
		n.Content = append(n.Content, &yaml.Node{
			Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle,
		})
	}
	return n
}

func entryType(item *yaml.Node) string { return scalar(mapGet(item, "type")) }
func entryPath(item *yaml.Node) string { return filepath.ToSlash(scalar(mapGet(item, "path"))) }

func entryExts(item *yaml.Node) []string {
	n := mapGet(item, "extensions")
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for _, c := range n.Content {
		if ext := normalizeExt(c.Value); ext != "" && ext != "." {
			out = append(out, ext)
		}
	}
	return out
}

func scalar(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}

func normalizeList(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, ext := range in {
		n := normalizeExt(ext)
		if n == "" || n == "." {
			return nil, fmt.Errorf("config: extension is invalid")
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("config: extension is invalid")
	}
	return out, nil
}

func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

func containsStr(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func subtract(have, drop []string) []string {
	var out []string
	for _, ext := range have {
		if !containsStr(drop, ext) {
			out = append(out, ext)
		}
	}
	return out
}

func saveDoc(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.Write(buf.Bytes())
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		os.Remove(name)
		if werr != nil {
			return werr
		}
		if serr != nil {
			return serr
		}
		return cerr
	}
	return os.Rename(name, path)
}
