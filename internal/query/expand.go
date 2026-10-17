package query

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/axitdev/onfly/internal/embedder"
)

// Expander asks a small local chat model which identifiers a question probably
// means. It bridges "decide whether a user is permitted to perform an action"
// to Gate, authorize and can, when the question repeats none of the code's words.
type Expander struct {
	Chat    embedder.Chat
	Timeout time.Duration

	mu    sync.Mutex
	cache map[string][]string
}

const (
	maxKeywords       = 8
	defaultExpandWait = 8 * time.Second
	expandCacheSize   = 256
)

const expandPrompt = `You help search the source code of a software project. A developer asked:

%s

List up to 8 identifiers or words that the code answering this question most likely contains: function, method, class or variable names, in the spelling the code would use (camelCase, PascalCase or snake_case), most specific first. Reply with a JSON array of strings and nothing else.`

// Expand returns the keywords for a question, from the cache when it was asked
// before. An error or an unusable answer returns no keywords.
func (e *Expander) Expand(ctx context.Context, q string) ([]string, error) {
	q = strings.TrimSpace(q)
	e.mu.Lock()
	if kws, ok := e.cache[q]; ok {
		e.mu.Unlock()
		return kws, nil
	}
	e.mu.Unlock()
	wait := e.Timeout
	if wait <= 0 {
		wait = defaultExpandWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	out, err := e.Chat.Complete(ctx, strings.Replace(expandPrompt, "%s", q, 1))
	if err != nil {
		return nil, err
	}
	kws := ParseKeywords(out)
	e.mu.Lock()
	if e.cache == nil || len(e.cache) >= expandCacheSize {
		e.cache = map[string][]string{}
	}
	e.cache[q] = kws
	e.mu.Unlock()
	return kws, nil
}

var (
	jsonArray = regexp.MustCompile(`(?s)\[.*\]`)
	keywordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{2,39}$`)
)

// ParseKeywords reads the model's JSON array and keeps the entries that look
// like identifiers; anything else the model wrote around or inside it is dropped.
func ParseKeywords(out string) []string {
	m := jsonArray.FindString(out)
	if m == "" {
		return nil
	}
	var raw []any
	if json.Unmarshal([]byte(m), &raw) != nil {
		return nil
	}
	var kws []string
	seen := map[string]bool{}
	for _, v := range raw {
		s, ok := v.(string)
		s = strings.TrimSpace(s)
		if !ok || !keywordRe.MatchString(s) || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		kws = append(kws, s)
		if len(kws) == maxKeywords {
			break
		}
	}
	return kws
}
