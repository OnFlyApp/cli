package index

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

var (
	callRef = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])([A-Za-z_][A-Za-z0-9_]*)\(`)
	selRef  = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*)`)
)

// mentioned returns symbol names used as calls or selectors in content.
// The chunk's own name is omitted. names is the set of metadata.name values.
func mentioned(content, self string, names map[string]struct{}) []string {
	found := map[string]struct{}{}
	add := func(name string) {
		if name == "" || name == self {
			return
		}
		if _, ok := names[name]; !ok {
			return
		}
		found[name] = struct{}{}
	}
	for _, m := range callRef.FindAllStringSubmatch(content, -1) {
		add(m[1])
	}
	for _, m := range selRef.FindAllStringSubmatch(content, -1) {
		add(m[1])
	}
	out := make([]string, 0, len(found))
	for name := range found {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func keepQualified(content string, existing, next []string) []string {
	seen := map[string]bool{}
	for _, name := range next {
		seen[name] = true
	}
	for _, ref := range existing {
		if ref == "" || seen[ref] || !strings.ContainsAny(ref, `.\\`) || !strings.Contains(content, ref) {
			continue
		}
		next = append(next, ref)
		seen[ref] = true
	}
	sort.Strings(next)
	return next
}

func sameRefs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func (s *Session) linkRefs(ctx context.Context) error {
	if s == nil || s.store == nil || s.opt.DryRun {
		return nil
	}
	rows, err := s.store.ListChunks(ctx)
	if err != nil {
		return err
	}
	names := map[string]struct{}{}
	for _, row := range rows {
		if n := row.Chunk.Metadata["name"]; n != "" {
			names[n] = struct{}{}
		}
	}
	updates := map[int64][]string{}
	for _, row := range rows {
		self := ""
		if row.Chunk.Metadata != nil {
			self = row.Chunk.Metadata["name"]
		}
		next := keepQualified(row.Chunk.Content, row.Chunk.Outbound, mentioned(row.Chunk.Content, self, names))
		if sameRefs(next, row.Chunk.Outbound) {
			continue
		}
		updates[row.ID] = next
	}
	return s.store.UpdateOutbound(ctx, updates)
}
