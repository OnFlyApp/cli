package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestExtHelpListsInstall(t *testing.T) {
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"ext", "--help"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "install") {
		t.Fatal(buf.String())
	}
}
