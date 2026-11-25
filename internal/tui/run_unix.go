//go:build unix

package tui

import (
	"os/exec"
	"syscall"
)

// detach starts rondo in its own session: without a controlling terminal, a tool that opens
// /dev/tty fails instead of drawing over the TUI.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
