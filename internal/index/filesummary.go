package index

import (
	"context"
	"fmt"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
)

// FileSummaryType is the type of the chunk that stands for a whole file: its
// path, namespace, types and the names of what it defines. A question about
// what a file is for ("where is the smallest payment worked out") shares words
// with that list, not with the body of any one method.
const FileSummaryType = parser.FileSummaryType

const (
	maxSummaryNames = 40
	summaryBatch    = 128
)

var summaryTypes = map[string]bool{
	"class": true, "interface": true, "trait": true, "struct": true, "enum": true, "type": true,
}

var summaryFuncs = map[string]bool{"function": true, "method": true}

// FileSummary describes a file by its chunks. The second result is false for a
// file with nothing named in it (documents, configuration).
func FileSummary(rel string, chunks []parser.Chunk) (parser.Chunk, bool) {
	var types, funcs []string
	seen := map[string]bool{}
	namespace := ""
	for _, c := range chunks {
		if c.Type == FileSummaryType {
			return parser.Chunk{}, false
		}
		name := c.Metadata["name"]
		if namespace == "" {
			namespace = c.Metadata["namespace"]
		}
		if name == "" || seen[c.Type+"|"+name] {
			continue
		}
		seen[c.Type+"|"+name] = true
		switch t := strings.ToLower(c.Type); {
		case summaryTypes[t]:
			types = append(types, name)
		case summaryFuncs[t]:
			funcs = append(funcs, name)
		}
	}
	if len(types)+len(funcs) == 0 {
		return parser.Chunk{}, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "File %s\n", rel)
	if namespace != "" {
		fmt.Fprintf(&b, "Namespace: %s\n", namespace)
	}
	if len(types) > 0 {
		fmt.Fprintf(&b, "Types: %s\n", strings.Join(types[:min(len(types), maxSummaryNames)], ", "))
	}
	if len(funcs) > 0 {
		more := ""
		if len(funcs) > maxSummaryNames {
			more = fmt.Sprintf(" (+%d more)", len(funcs)-maxSummaryNames)
		}
		fmt.Fprintf(&b, "Defines: %s%s\n", strings.Join(funcs[:min(len(funcs), maxSummaryNames)], ", "), more)
	}
	return parser.Chunk{
		FilePath: rel,
		Type:     FileSummaryType,
		Content:  strings.TrimRight(b.String(), "\n"),
		Metadata: map[string]string{"start_line": "1", "end_line": "1"},
	}, true
}

// addFileSummaries gives the files indexed before file summaries existed their
// summary. Only the summaries are embedded; the chunks a file already has,
// their vectors and its hash are left as they are.
func (s *Session) addFileSummaries(ctx context.Context) error {
	if s.opt.Lexical {
		return nil // a summary is for the vectors; a lexical index has none
	}
	files, err := s.store.FilesWithoutType(ctx, FileSummaryType)
	if err != nil {
		return err
	}
	for start := 0; start < len(files); start += summaryBatch {
		batch := files[start:min(start+summaryBatch, len(files))]
		rows, err := s.store.ChunksByFiles(ctx, batch)
		if err != nil {
			return err
		}
		byFile := map[string][]parser.Chunk{}
		for _, r := range rows {
			byFile[r.Chunk.FilePath] = append(byFile[r.Chunk.FilePath], r.Chunk)
		}
		var sums []parser.Chunk
		for _, f := range batch {
			if sum, ok := FileSummary(f, byFile[f]); ok {
				sums = append(sums, sum)
			}
		}
		if len(sums) == 0 {
			continue
		}
		s.report("file summaries", batch[0])
		embedded := make([]vectorstore.EmbeddedChunk, len(sums))
		for i, c := range sums {
			embedded[i] = vectorstore.EmbeddedChunk{Chunk: c, Workspace: s.cfg.Workspace}
		}
		if !s.opt.Lexical && s.emb != nil {
			got, err := embedChunks(ctx, s.emb, sums, summaryBatch, s.cfg.Indexer.WithContext())
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s.logger.Warn("file summaries stored without vectors", "err", err)
			} else {
				for i := range got {
					embedded[i].Vector = got[i].Vector
					embedded[i].Workspace = s.cfg.Workspace
				}
			}
		}
		if err := s.store.AddChunks(ctx, embedded); err != nil {
			return err
		}
	}
	return nil
}
