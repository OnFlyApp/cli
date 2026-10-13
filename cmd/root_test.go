package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpMentionsIndex(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--help"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "onfly index") {
		t.Fatalf("help missing onfly index:\n%s", buf.String())
	}
}
