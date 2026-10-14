package vectorstore

import "testing"

func TestParseVecLiteralRoundTrip(t *testing.T) {
	in := []float32{1, -0.5, 3.25, 0}
	got, err := parseVecLiteral(vecLiteral(in))
	if err != nil || len(got) != len(in) {
		t.Fatalf("%v %v", got, err)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("%v != %v", got, in)
		}
	}
	if _, err := parseVecLiteral("nope"); err == nil {
		t.Fatal("bad literal must fail")
	}
}
