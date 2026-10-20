package query

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
	"github.com/axitdev/onfly/internal/vectorstore"
	"github.com/axitdev/onfly/internal/vectorstore/storetest"
)

type fakeChat struct {
	answer string
	calls  atomic.Int32
}

func (f *fakeChat) Complete(context.Context, string) (string, error) {
	f.calls.Add(1)
	return f.answer, nil
}

func TestParseKeywordsKeepsIdentifiersOnly(t *testing.T) {
	got := ParseKeywords("Sure! Here you go:\n```json\n[\"CanAccess\", \"policy_check\", \"two words\", \"a\", 7, \"canaccess\", \"Gate\", \"../etc\"]\n```")
	if want := []string{"CanAccess", "policy_check", "Gate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
	for _, bad := range []string{"", "no array here", "[1, 2", `{"a": 1}`} {
		if ParseKeywords(bad) != nil {
			t.Errorf("%q should give nothing", bad)
		}
	}
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, fmt.Sprintf("\"name%d\"", i))
	}
	if len(ParseKeywords("["+strings.Join(many, ",")+"]")) != maxKeywords {
		t.Fatal("the list is capped")
	}
}

func TestExpanderCachesAnswers(t *testing.T) {
	chat := &fakeChat{answer: `["Alpha"]`}
	e := &Expander{Chat: chat}
	for i := 0; i < 3; i++ {
		if got, err := e.Expand(context.Background(), "a question"); err != nil || len(got) != 1 {
			t.Fatalf("%v %v", got, err)
		}
	}
	if chat.calls.Load() != 1 {
		t.Fatalf("the model was asked %d times", chat.calls.Load())
	}
}

// The answer is in policy.go under names the question does not use.
func expansionCorpus(t *testing.T, chat *fakeChat) *Searcher {
	t.Helper()
	st, err := vectorstore.OpenSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Init(ctx, 4); err != nil {
		t.Fatal(err)
	}
	var chunks []vectorstore.EmbeddedChunk
	for i := 0; i < 30; i++ {
		chunks = append(chunks, vectorstore.EmbeddedChunk{
			Chunk:  parser.Chunk{FilePath: fmt.Sprintf("web/handler%02d.go", i), Type: "function", Content: "func handle() { // a user may perform an action here }", Metadata: map[string]string{"name": fmt.Sprintf("handle%02d", i)}},
			Vector: []float32{1, 0, 0, 1},
		})
	}
	chunks = append(chunks, vectorstore.EmbeddedChunk{
		Chunk:  parser.Chunk{FilePath: "auth/policy.go", Type: "function", Content: "func CanAccess(subject, ability string) bool { return grants[subject][ability] }", Metadata: map[string]string{"name": "CanAccess"}},
		Vector: []float32{0, 1, 0, 0},
	})
	if err := storetest.Save(ctx, st, chunks); err != nil {
		t.Fatal(err)
	}
	s := &Searcher{Store: st, Emb: vecEmb{v: []float32{1, 0, 0, 1}}}
	if chat != nil {
		s.Expander = &Expander{Chat: chat}
	}
	return s
}

func inTop(cs []parser.Chunk, file string) bool {
	for _, c := range cs {
		if c.FilePath == file {
			return true
		}
	}
	return false
}

func TestFocusExpandsAQuestionThatSharesNoWordsWithTheCode(t *testing.T) {
	q := Request{Query: "decide whether a user is permitted to perform an action", Limit: 5}
	plain, err := expansionCorpus(t, nil).RunFocus(context.Background(), q)
	if err != nil || inTop(plain, "auth/policy.go") {
		t.Fatalf("without expansion the answer is not found: %v %v", err, plain)
	}
	chat := &fakeChat{answer: `["CanAccess", "policy"]`}
	got, err := expansionCorpus(t, chat).RunFocus(context.Background(), q)
	if err != nil || !inTop(got, "auth/policy.go") {
		t.Fatalf("expansion finds it: %v %v", err, got)
	}
	if chat.calls.Load() != 1 {
		t.Fatalf("%d calls", chat.calls.Load())
	}
}

func TestFocusDoesNotAskTheModelWhenTheTopResultIsNamedForTheQuestion(t *testing.T) {
	chat := &fakeChat{answer: `["Other"]`}
	s := expansionCorpus(t, chat)
	s.Emb = vecEmb{v: []float32{0, 1, 0, 0}} // the vectors agree with the name
	got, err := s.RunFocus(context.Background(), Request{Query: "access policy", Limit: 5})
	if err != nil || !inTop(got, "auth/policy.go") {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].FilePath != "auth/policy.go" {
		t.Fatalf("%v", got[0].FilePath)
	}
	if chat.calls.Load() != 0 {
		t.Fatalf("a confident answer needs no expansion, the model was asked %d times", chat.calls.Load())
	}
}

func TestAModelThatFailsLeavesTheAnswerAlone(t *testing.T) {
	s := expansionCorpus(t, &fakeChat{answer: "I cannot help with that."})
	if _, err := s.RunFocus(context.Background(), Request{Query: "decide whether a user is permitted to perform an action", Limit: 5}); err != nil {
		t.Fatal(err)
	}
}
