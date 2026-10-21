package skeleton

import (
	"strconv"
	"strings"
	"testing"
)

const phpSrc = `<?php

namespace App\Services;

final class PaymentService
{
    /** @var array<string, int> */
    private array $limits = ['a' => 1];

    /**
     * Charge a card.
     *
     * @param array{amount: int} $payload
     */
    #[Route('/charge', methods: ['POST'])]
    #[Audit(
        level: 'high',
    )]
    public function charge(
        int $id,
        array $payload,
    ): bool
    {
        $a = 1;
        $b = 2;
        $c = 3;
        $d = 4;
        return $a + $b + $c + $d > 0;
    }

    public function tiny(): int { return 1; }

    /**
     * Refund.
     */
    public function refund(int $id): void {
        $a = 1;
        $b = 2;
        $c = 3;
        $d = 4;
        $e = 5;
        $this->log($a + $b + $c + $d + $e);
    }
}
`

const goSrc = `package pay

// User has json tags that must survive.
type User struct {
	ID   int    ` + "`json:\"id\"`" + `
	Name string ` + "`json:\"name\"`" + `
}

// Charge bills a user.
func (s *Service) Charge(
	ctx context.Context,
	u User,
) error {
	a := 1
	b := 2
	c := 3
	d := 4
	return use(a, b, c, d)
}

func Short() { return }

func Last() {
	x := 1
	y := 2
	z := 3
	_ = x + y + z
}
`

func TestCollapse(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		spans   []Span
		focus   string
		min     int
		keep    []string
		drop    []string
		markers int
	}{
		{
			name:  "php docblock, attributes, multi-line signature, brace on its own line",
			src:   phpSrc,
			spans: []Span{{10, 29, "charge"}, {31, 31, "tiny"}, {33, 43, "refund"}},
			min:   6,
			keep: []string{
				"     * @param array{amount: int} $payload",
				"    #[Route('/charge', methods: ['POST'])]",
				"    #[Audit(",
				"        level: 'high',",
				"        array $payload,",
				"    ): bool",
				"    public function tiny(): int { return 1; }",
				"    public function refund(int $id): void {",
				"    private array $limits = ['a' => 1];",
			},
			drop:    []string{"$a = 1;", "return $a + $b", "$this->log("},
			markers: 2,
		},
		{
			name:  "focus keeps one body and still collapses the others",
			src:   phpSrc,
			spans: []Span{{10, 29, "charge"}, {33, 43, "refund"}},
			focus: "refund",
			min:   6,
			keep:  []string{"$this->log($a + $b + $c + $d + $e);", "public function charge("},
			drop:  []string{"return $a + $b + $c + $d > 0;"},

			markers: 1,
		},
		{
			name:  "go keeps tags and doc comments, several bodies replaced without shifting",
			src:   goSrc,
			spans: []Span{{9, 19, "Charge"}, {21, 21, "Short"}, {23, 28, "Last"}},
			min:   4,
			keep: []string{
				"// User has json tags that must survive.",
				"	ID   int    `json:\"id\"`",
				"// Charge bills a user.",
				"func (s *Service) Charge(",
				"	ctx context.Context,",
				") error {",
				"func Short() { return }",
				"func Last() {",
			},
			drop:    []string{"return use(a, b, c, d)", "x := 1"},
			markers: 2,
		},
		{
			name:  "spans given bottom-up or nested are handled",
			src:   goSrc,
			spans: []Span{{23, 28, "Last"}, {9, 19, "Charge"}, {12, 14, "Inner"}},
			min:   4,
			keep:  []string{"func Last() {", ") error {"},
			drop:  []string{"x := 1", "a := 1"},

			markers: 2,
		},
		{
			name:  "a span without a body brace is left whole",
			src:   "def f(a):\n    x = 1\n    y = 2\n    return x + y\n",
			spans: []Span{{1, 4, "f"}},
			min:   2,
			keep:  []string{"x = 1", "return x + y"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Collapse(tc.src, tc.spans, tc.focus, tc.min)
			for _, k := range tc.keep {
				if !strings.Contains(got, k) {
					t.Errorf("lost %q in\n%s", k, got)
				}
			}
			for _, d := range tc.drop {
				if strings.Contains(got, d) {
					t.Errorf("kept %q in\n%s", d, got)
				}
			}
			if n := strings.Count(got, "body omitted"); n != tc.markers {
				t.Errorf("%d markers, want %d\n%s", n, tc.markers, got)
			}
			for _, line := range strings.Split(got, "\n") {
				if !strings.Contains(line, "body omitted") && !strings.Contains(tc.src, line) {
					t.Errorf("line not copied from the source: %q", line)
				}
			}
		})
	}
}

func TestCollapseReportsOriginalLineNumbers(t *testing.T) {
	got := Collapse(goSrc, []Span{{9, 19, "Charge"}, {23, 28, "Last"}}, "", 4)
	for _, want := range []string{"lines 14-18", "lines 24-27"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestCollapseDoesNotMutateSpans(t *testing.T) {
	spans := []Span{{23, 28, "Last"}, {9, 19, "Charge"}}
	Collapse(goSrc, spans, "", 4)
	if spans[0].Name != "Last" {
		t.Fatalf("caller's slice was reordered: %+v", spans)
	}
}

const goTypeLiteralSrc = `package p

// Process has type literals all over its signature.
func Process(
	ctx context.Context,
	meta map[string]interface{},
	opt struct {
		Retries int
	},
	fn func() struct{},
) (out map[string]interface{}, err error) {
	a := 1
	b := 2
	c := 3
	return nil, nil
}
`

const phpStringSrc = `<?php

class ItemController
{
    /**
     * @return array{id: int}
     */
    #[Route('/api/v1/items/{id}', methods: ['GET'])]
    public function show(
        int $id,
        string $tpl = '{default}',
        string $other = "{also}",
    ): array {
        $a = 1;
        $b = 2;
        $c = 3;
        return ['id' => $id];
    }
}
`

func TestCollapseSignaturesWithBracesInsideThem(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		span  Span
		keep  []string
		drop  []string
		lines string
	}{
		{
			name: "go: interface{} and struct literals in parameters and results",
			src:  goTypeLiteralSrc,
			span: Span{4, 16, "Process"},
			keep: []string{
				"	meta map[string]interface{},",
				"	opt struct {",
				"		Retries int",
				"	fn func() struct{},",
				") (out map[string]interface{}, err error) {",
			},
			drop:  []string{"a := 1", "return nil, nil"},
			lines: "lines 12-15",
		},
		{
			name: "php: braces in an attribute path and in default string values",
			src:  phpStringSrc,
			span: Span{5, 18, "show"},
			keep: []string{
				"     * @return array{id: int}",
				"    #[Route('/api/v1/items/{id}', methods: ['GET'])]",
				"        string $tpl = '{default}',",
				`        string $other = "{also}",`,
				"    ): array {",
			},
			drop:  []string{"$a = 1", "return ['id' => $id];"},
			lines: "lines 14-17",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Collapse(tc.src, []Span{tc.span}, "", 4)
			for _, k := range tc.keep {
				if !strings.Contains(got, k) {
					t.Errorf("lost %q in\n%s", k, got)
				}
			}
			for _, d := range tc.drop {
				if strings.Contains(got, d) {
					t.Errorf("kept %q in\n%s", d, got)
				}
			}
			if !strings.Contains(got, "body omitted: "+tc.lines) {
				t.Errorf("want marker %q in\n%s", tc.lines, got)
			}
		})
	}
}

func TestSignatureSkipsDocsAndAttributes(t *testing.T) {
	tests := map[string]string{
		"// Doc line.\n// More.\nfunc Run(a int) error {\n\treturn nil\n}":      "func Run(a int) error {",
		"/**\n * Doc.\n */\n#[Route('/x')]\npublic function show(): void\n{\n}": "public function show(): void",
		"type Span struct {\n\tStart int\n}":                                    "type Span struct {",
		"// only a comment":                                                     "// only a comment",
	}
	for in, want := range tests {
		if got := Signature(in); got != want {
			t.Errorf("Signature(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollapseNumberedKeepsTheNumbersOfTheOriginalFile(t *testing.T) {
	var src strings.Builder
	src.WriteString("package p\n\n// Big does a lot.\nfunc Big() int {\n")
	for i := 0; i < 12; i++ {
		src.WriteString("\tx := 1\n")
	}
	src.WriteString("\treturn x\n}\n\nfunc Small() {}\n")
	text := src.String()
	original := strings.Split(text, "\n")
	// Big is lines 4-18: signature on 4, body 5-17, closing brace on 18.
	spans := []Span{{Start: 4, End: 18, Name: "Big"}, {Start: 20, End: 20, Name: "Small"}}
	got := CollapseNumbered(text, spans, "", 10)
	lines := strings.Split(got, "\n")
	if lines[0] != ExpandTip {
		t.Fatalf("a collapsed body comes with the tip first: %q", lines[0])
	}
	marker := ""
	for _, l := range lines[1:] {
		num, rest, ok := strings.Cut(l, "| ")
		if !ok {
			t.Fatalf("every line has a number: %q", l)
		}
		n, err := strconv.Atoi(strings.TrimSpace(num))
		if err != nil {
			t.Fatalf("%q", l)
		}
		if strings.Contains(rest, "body omitted") {
			marker = l
			continue
		}
		if rest != original[n-1] {
			t.Fatalf("line %d of the skeleton is %q, line %d of the file is %q", n, rest, n, original[n-1])
		}
	}
	for _, want := range []string{" 5| ", "lines 5-17", "~62 tokens", `focus_symbol="Big"`} {
		if !strings.Contains(marker, want) {
			t.Fatalf("marker %q lacks %q", marker, want)
		}
	}
	if !strings.Contains(got, "18| }") || !strings.Contains(got, "20| func Small() {}") {
		t.Fatalf("lines after a collapsed body keep their own numbers:\n%s", got)
	}
	// Nothing collapsed, no tip.
	if plain := CollapseNumbered("a\nb\n", nil, "", 10); strings.Contains(plain, "Tip") || !strings.HasPrefix(plain, "1| a\n2| b") {
		t.Fatalf("%q", plain)
	}
}

func TestCollapsePreambleKeepsTheNumbersAndThePackageLine(t *testing.T) {
	var src strings.Builder
	src.WriteString("<?php\n/*\n * Licence line one\n * Licence line two\n */\nnamespace App\\Thing;\n\nuse A\\B;\nuse A\\C;\nuse A\\D;\n\n/** Docs. */\nfunction f() {}\n")
	numbered := CollapseNumbered(src.String(), nil, "", 100)
	got := CollapsePreamble(numbered, 12, 6, 6)
	for _, want := range []string{" 1| // ... [lines 1-5: header and imports omitted] ...", " 6| namespace App\\Thing;", " 7| // ... [lines 7-11: header and imports omitted] ...", "12| /** Docs. */", "13| function f() {}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "Licence line") || strings.Contains(got, "use A") {
		t.Fatalf("the preamble is hidden:\n%s", got)
	}
	if short := CollapsePreamble(numbered, 4, 0, 6); short != numbered {
		t.Fatal("a short preamble is left alone")
	}
	// The tip line of a skeleton with collapsed bodies is not a numbered line and stays.
	withTip := ExpandTip + "\n" + numbered
	if !strings.HasPrefix(CollapsePreamble(withTip, 12, 6, 6), ExpandTip+"\n") {
		t.Fatal("the tip stays first")
	}
}
