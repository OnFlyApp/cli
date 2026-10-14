package watch

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("ONFLY_ALLOW_STDIO", "1")
	os.Exit(m.Run())
}
