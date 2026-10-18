package index

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/axitdev/onfly/internal/vectorstore"
)

var (
	callRef = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])([A-Za-z_][A-Za-z0-9_]*)\(`)
	selRef  = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*)`)
	// A class a PHP method takes as a typed parameter (constructor injection
	// included) or declares in @throws is a reference to it, so looking up a
	// service finds every handler that receives it.
	typedParamRef = regexp.MustCompile(`[(,]\s*(?:(?:private|protected|public|readonly)\s+)*\??\\?(?:\w+\\)*([A-Z]\w*)\s+(?:\.\.\.)?&?\$\w`)
	throwsRef     = regexp.MustCompile(`@throws\s+\\?(?:\w+\\)*([A-Z]\w*)`)
)

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
	for _, re := range []*regexp.Regexp{typedParamRef, throwsRef} {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			add(m[1])
		}
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

func (s *Session) linkRefs(ctx context.Context, changed []string) error {
	if s == nil || s.store == nil || s.opt.DryRun {
		return nil
	}
	list, err := s.store.Names(ctx)
	if err != nil {
		return err
	}
	names := make(map[string]struct{}, len(list))
	for _, n := range list {
		names[n] = struct{}{}
	}
	full := changed == nil || !sameSet(s.names, names)
	s.names = names
	var rows []vectorstore.StoredChunk
	switch {
	case full:
		rows, err = s.store.ListChunks(ctx)
	case len(changed) == 0:
		return nil
	default:
		rows, err = s.store.ChunksByFiles(ctx, changed)
	}
	if err != nil {
		return err
	}
	updates := map[int64][]string{}
	for _, row := range rows {
		self := row.Chunk.Metadata["name"]
		next := keepQualified(row.Chunk.Content, row.Chunk.Outbound, mentioned(row.Chunk.Content, self, names))
		if sameRefs(next, row.Chunk.Outbound) {
			continue
		}
		updates[row.ID] = next
	}
	return s.store.UpdateOutbound(ctx, updates)
}

func sameSet(a, b map[string]struct{}) bool {
	if a == nil || len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}
