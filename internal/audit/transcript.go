package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Transcript is what an assistant's session logs say about how it read the
// project: how often it used each tool, and which files it read whole.
type Transcript struct {
	Sessions int            `json:"sessions"`
	Tools    map[string]int `json:"tool_calls"`
	// FullReads counts reads of a file with no offset and no limit.
	FullReads map[string]int `json:"full_reads"`
}

// ProjectDir is where Claude Code keeps the session logs of the project at
// root: a directory of the home folder named after the path with its slashes
// and dots turned into dashes.
func ProjectDir(root string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	slug := strings.NewReplacer("/", "-", ".", "-", "\\", "-", ":", "-").Replace(root)
	return filepath.Join(home, ".claude", "projects", slug)
}

type line struct {
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Input struct {
		FilePath string `json:"file_path"`
		Offset   *int   `json:"offset"`
		Limit    *int   `json:"limit"`
	} `json:"input"`
}

// ReadTranscripts reads every .jsonl session log in dir, keeping the events
// since a time (zero for all). root turns absolute paths into project-relative
// ones; reads of files outside it are ignored.
func ReadTranscripts(dir, root string, since time.Time) (Transcript, error) {
	t := Transcript{Tools: map[string]int{}, FullReads: map[string]int{}}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return t, err
	}
	for _, f := range files {
		used, err := readSession(f, root, since, &t)
		if err != nil {
			continue
		}
		if used {
			t.Sessions++
		}
	}
	return t, nil
}

func readSession(path, root string, since time.Time, t *Transcript) (bool, error) {
	fh, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	used := false
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil || len(l.Message.Content) == 0 || l.Message.Content[0] != '[' {
			continue
		}
		if !since.IsZero() && l.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil && ts.Before(since) {
				continue
			}
		}
		var blocks []block
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			used = true
			t.Tools[b.Name]++
			if b.Name == "Read" && b.Input.Offset == nil && b.Input.Limit == nil {
				if rel, ok := relative(root, b.Input.FilePath); ok {
					t.FullReads[rel]++
				}
			}
		}
	}
	return used, sc.Err()
}

func relative(root, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	rel := p
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(root, p)
		if err != nil || strings.HasPrefix(r, "..") {
			return "", false
		}
		rel = r
	}
	return filepath.ToSlash(rel), true
}

// OnflyCalls counts the calls to the Onfly tools, whatever the server is named.
func (t Transcript) OnflyCalls() int {
	n := 0
	for name, c := range t.Tools {
		if strings.Contains(name, "onfly_") {
			n += c
		}
	}
	return n
}

// SearchCalls counts Grep and Glob calls, the ones onfly_focus replaces.
func (t Transcript) SearchCalls() int { return t.Tools["Grep"] + t.Tools["Glob"] }
