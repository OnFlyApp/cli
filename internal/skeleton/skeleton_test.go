package skeleton

import (
	"strings"
	"testing"
)

const src = `package p

func Long() {
	a := 1
	b := 2
	c := 3
	_ = a + b + c
}

func Short() { return }

func Focus() {
	x := 1
	y := 2
	z := 3
	_ = x + y + z
}
`

func TestCollapseKeepsShortAndFocus(t *testing.T) {
	spans := []Span{{3, 8, "Long"}, {10, 10, "Short"}, {12, 17, "Focus"}}
	got := Collapse(src, spans, "Focus", 4)
	if strings.Contains(got, "a := 1") || !strings.Contains(got, "body omitted: lines 4-7") {
		t.Fatalf("long body kept:\n%s", got)
	}
	if !strings.Contains(got, "func Long() {") || !strings.Contains(got, "func Short() { return }") || !strings.Contains(got, "x := 1") {
		t.Fatalf("lost lines:\n%s", got)
	}
	if strings.Count(got, "}") < 3 {
		t.Fatalf("closing lines lost:\n%s", got)
	}
}

func TestCollapseIgnoresNestedSpans(t *testing.T) {
	spans := []Span{{3, 8, "Long"}, {4, 6, "Inner"}}
	got := Collapse(src, spans, "", 2)
	if strings.Count(got, "body omitted") != 1 {
		t.Fatalf("%s", got)
	}
}
