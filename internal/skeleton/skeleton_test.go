package skeleton

import (
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
