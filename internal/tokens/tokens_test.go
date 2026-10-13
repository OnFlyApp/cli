package tokens

import "testing"

func TestCountStable(t *testing.T) {
	n, err := Count("hello")
	if err != nil || n < 1 {
		t.Fatalf("count = %d %v", n, err)
	}
	n2, err := Count("hello")
	if err != nil || n2 != n {
		t.Fatalf("unstable %d %d", n, n2)
	}
}
