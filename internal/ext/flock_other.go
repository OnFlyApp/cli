//go:build !unix && !windows

package ext

import (
	"fmt"
	"os"
)

func flockExclusive(*os.File) error {
	return fmt.Errorf("ext: file locking is not supported on this platform")
}

func flockUnlock(*os.File) error { return nil }
