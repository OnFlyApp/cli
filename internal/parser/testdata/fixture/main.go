package main

import (
	"encoding/json"
	"io"
	"os"
)

type chunk struct {
	FilePath string            `json:"file_path"`
	Type     string            `json:"type"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata"`
}

func main() {
	if len(os.Args) < 2 {
		os.Stderr.WriteString("missing path\n")
		os.Exit(1)
	}
	src, _ := io.ReadAll(os.Stdin)
	if string(src) == "FAIL" {
		os.Stderr.WriteString("boom\n")
		os.Exit(1)
	}
	if string(src) == "LOOP" {
		for {
		}
	}
	if string(src) == "BIG" {
		buf := make([]byte, 1<<20)
		for i := 0; i < 33; i++ {
			_, _ = os.Stdout.Write(buf)
		}
		os.Exit(0)
	}
	_ = json.NewEncoder(os.Stdout).Encode([]chunk{{
		FilePath: os.Args[1],
		Type:     "class",
		Content:  string(src),
		Metadata: map[string]string{"name": "A"},
	}})
}
