package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/axitdev/onfly/internal/parser"
)

// Chat is the one method of a chat model the judge needs.
type Chat interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// JudgeTop is how many results the judge looks at per case.
const JudgeTop = 5

const judgeSnippetRunes = 700

// Judged is the result of asking a model whether better-ranked hits also
// answer the question. A single expected chunk understates quality: a hit at
// rank 1 that is not the expected chunk may still be a correct answer.
type Judged struct {
	Total int
	// Alternatives counts cases whose expected chunk was not first but a hit
	// ranked above it (or any hit, for a miss) was judged to answer the question.
	Alternatives int
	// Recall and MRR count a hit as the expected chunk or a judged answer,
	// within JudgeTop results.
	Recall, MRR float64
	// Strict is the same measurement without the judge, at JudgeTop.
	StrictRecall, StrictMRR float64
}

var numRe = regexp.MustCompile(`\d+`)

// Judge asks chat which of each case's top results answer the question. Only
// cases whose expected chunk is not first are sent to the model.
func Judge(ctx context.Context, chat Chat, cases []Case, results map[string][]parser.Chunk, fileLevel bool) (Judged, error) {
	var j Judged
	j.Total = len(cases)
	for _, c := range cases {
		got := results[c.Query]
		if len(got) > JudgeTop {
			got = got[:JudgeTop]
		}
		r := Rank(c, got, JudgeTop, fileLevel)
		if r > 0 {
			j.StrictRecall++
			j.StrictMRR += 1 / float64(r)
		}
		best := r
		if r != 1 && len(got) > 0 {
			limit := len(got)
			if r > 0 {
				limit = r - 1 // only hits ranked above the expected one can improve it
			}
			if limit > 0 {
				rel, err := askJudge(ctx, chat, c.Query, got[:limit])
				if err != nil {
					return j, fmt.Errorf("judge %q: %w", c.Query, err)
				}
				for _, idx := range rel {
					if best == 0 || idx+1 < best {
						best = idx + 1
					}
				}
				if best != r {
					j.Alternatives++
				}
			}
		}
		if best > 0 {
			j.Recall++
			j.MRR += 1 / float64(best)
		}
	}
	if j.Total > 0 {
		n := float64(j.Total)
		j.Recall, j.MRR, j.StrictRecall, j.StrictMRR = j.Recall/n, j.MRR/n, j.StrictRecall/n, j.StrictMRR/n
	}
	return j, nil
}

// askJudge returns the 0-based indices of the results that answer the question.
func askJudge(ctx context.Context, chat Chat, question string, results []parser.Chunk) ([]int, error) {
	var b strings.Builder
	b.WriteString("A developer asked a question about a codebase. Below are code search results.\n")
	b.WriteString("Question: " + question + "\n\n")
	for i, c := range results {
		text := c.Content
		if r := []rune(text); len(r) > judgeSnippetRunes {
			text = string(r[:judgeSnippetRunes]) + " ..."
		}
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", i+1, c.FilePath, text)
	}
	b.WriteString("Which results contain the code or text that directly answers the question? ")
	b.WriteString("A result that only mentions related things does not count. ")
	b.WriteString("Answer with JSON only: {\"relevant\": [numbers]} (an empty list if none).")
	out, err := chat.Complete(ctx, b.String())
	if err != nil {
		return nil, err
	}
	return parseRelevant(out, len(results)), nil
}

// parseRelevant reads {"relevant":[...]} (or any text with numbers) into
// 0-based indices within n, without repeats.
func parseRelevant(out string, n int) []int {
	var nums []int
	var obj struct {
		Relevant []int `json:"relevant"`
	}
	text := out
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}
	if json.Unmarshal([]byte(text), &obj) == nil {
		nums = obj.Relevant
	} else {
		for _, m := range numRe.FindAllString(out, -1) {
			if v, err := strconv.Atoi(m); err == nil {
				nums = append(nums, v)
			}
		}
	}
	seen := make([]bool, n)
	var res []int
	for _, v := range nums {
		if v >= 1 && v <= n && !seen[v-1] {
			seen[v-1] = true
			res = append(res, v-1)
		}
	}
	return res
}
