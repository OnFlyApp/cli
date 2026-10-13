package split

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/axitdev/onfly/internal/parser"
)

func Split(chunks []parser.Chunk, limit, overlap int, count func(string) int) []parser.Chunk {
	if limit < 1 {
		return chunks
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= limit {
		overlap = limit - 1
	}
	var out []parser.Chunk
	for _, ch := range chunks {
		if count(ch.Content) <= limit {
			out = append(out, ch)
			continue
		}
		parts := applyOverlap(splitText(ch.Content, limit, count), limit, overlap, count)
		n := len(parts)
		for i, part := range parts {
			next := ch
			next.Content = part
			next.Metadata = copyMeta(ch.Metadata)
			if ch.Outbound != nil {
				next.Outbound = append([]string(nil), ch.Outbound...)
			}
			if n > 1 {
				next.Metadata["part"] = fmt.Sprintf("%d/%d", i+1, n)
			}
			if i > 0 {
				next.Outbound = nil
			}
			out = append(out, next)
		}
	}
	return out
}

func copyMeta(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func splitText(text string, limit int, count func(string) int) []string {
	return splitWith(text, []string{"\n\n", "\n", "{", "}", " "}, limit, count)
}

func splitWith(text string, seps []string, limit int, count func(string) int) []string {
	if text == "" {
		return nil
	}
	if count(text) <= limit {
		return []string{text}
	}
	if len(seps) == 0 {
		return hardWrap(text, limit, count)
	}
	sep := seps[0]
	raw := strings.Split(text, sep)
	var pieces []string
	for i, part := range raw {
		if i < len(raw)-1 {
			part += sep
		}
		if part == "" {
			continue
		}
		if count(part) > limit {
			pieces = append(pieces, splitWith(part, seps[1:], limit, count)...)
			continue
		}
		pieces = append(pieces, part)
	}
	var packed []string
	buf := ""
	for _, piece := range pieces {
		if buf == "" {
			buf = piece
			continue
		}
		if count(buf+piece) <= limit {
			buf += piece
			continue
		}
		packed = append(packed, buf)
		buf = piece
	}
	if buf != "" {
		packed = append(packed, buf)
	}
	if len(packed) == 0 {
		return hardWrap(text, limit, count)
	}
	return packed
}

func hardWrap(text string, limit int, count func(string) int) []string {
	var out []string
	rest := text
	for rest != "" {
		if count(rest) <= limit {
			out = append(out, rest)
			break
		}
		best := 1
		lo, hi := 1, len(rest)
		for lo <= hi {
			mid := (lo + hi) / 2
			if count(rest[:mid]) <= limit {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best < 1 {
			best = 1
		}
		for best > 1 && !isBoundary(rest, best) {
			best--
		}
		for best > 1 && count(rest[:best]) > limit {
			best--
			for best > 1 && !isBoundary(rest, best) {
				best--
			}
		}
		if best > len(rest) {
			best = len(rest)
		}
		if count(rest[:best]) > limit {
			_, size := utf8.DecodeRuneInString(rest)
			if size < 1 {
				size = 1
			}
			best = size
		}
		out = append(out, rest[:best])
		rest = rest[best:]
	}
	return out
}

func isBoundary(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	return (s[i] & 0xC0) != 0x80
}

func applyOverlap(parts []string, limit, overlap int, count func(string) int) []string {
	if overlap == 0 || len(parts) < 2 {
		return parts
	}
	out := []string{parts[0]}
	for i := 1; i < len(parts); i++ {
		suffix := tail(out[len(out)-1], overlap, count)
		for suffix != "" && count(suffix+parts[i]) > limit {
			_, size := utf8.DecodeRuneInString(suffix)
			if size < 1 {
				size = 1
			}
			suffix = suffix[size:]
		}
		merged := suffix + parts[i]
		if count(merged) > limit {
			merged = parts[i]
		}
		out = append(out, merged)
	}
	return out
}

func tail(s string, overlap int, count func(string) int) string {
	if s == "" || count(s) <= overlap {
		return s
	}
	best := s
	lo, hi := 1, len(s)
	for lo <= hi {
		mid := (lo + hi) / 2
		suf := s[len(s)-mid:]
		if count(suf) >= overlap {
			best = suf
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	start := len(s) - len(best)
	for start > 0 && !isBoundary(s, start) {
		start--
	}
	return s[start:]
}
