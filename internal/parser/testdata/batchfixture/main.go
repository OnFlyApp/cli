package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
)

// Answers batch requests: the chunk's name is how many files this process has
// served, "FAIL" is a parse error and "LOOP" never answers.
func main() {
	out := json.NewEncoder(os.Stdout)
	_ = out.Encode(map[string]string{"ready": "onfly-batch/1"})
	in := bufio.NewReader(os.Stdin)
	n := 0
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			Path    string `json:"path"`
			Content []byte `json:"content"`
		}
		if json.Unmarshal(line, &req) != nil {
			return
		}
		n++
		switch string(req.Content) {
		case "FAIL":
			_ = out.Encode(map[string]string{"error": "boom"})
		case "DIE":
			os.Exit(3)
		case "LOOP":
			select {}
		default:
			_ = out.Encode(map[string]any{"chunks": []map[string]any{{
				"type": "class", "content": string(req.Content), "metadata": map[string]string{"name": strconv.Itoa(n)},
			}}})
		}
	}
}
