package index

import "testing"

func TestSmallEmbeddingPrice(t *testing.T) {
	usd, known := EstimateUSD("text-embedding-3-small", 1_000_000)
	if !known || usd != 0.02 {
		t.Fatalf("%v %v", usd, known)
	}
	_, known = EstimateUSD("nomic-embed-text", 100)
	if known {
		t.Fatal("ollama model should not have a cloud price")
	}
	usd, known = EstimateUSD("voyage-code-3", 1_000_000)
	if !known || usd != 0.18 {
		t.Fatalf("voyage-code-3 %v %v", usd, known)
	}
	usd, known = EstimateUSD("voyage-4-lite", 2_000_000)
	if !known || usd != 0.04 {
		t.Fatalf("voyage-4-lite %v %v", usd, known)
	}
	if _, known = EstimateUSD("qwen3-embedding:0.6b", 100); known {
		t.Fatal("local model should not have a cloud price")
	}
}
