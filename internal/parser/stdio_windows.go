//go:build windows

package parser

import (
	"os/exec"
	"time"
)

func prepareCmd(cmd *exec.Cmd) {
	cmd.WaitDelay = time.Second
}
