//go:build !windows

package detach

import (
	"os"
	"os/exec"
	"syscall"
)

// startDetached starts cmd in its own session, so a hangup or Ctrl+C aimed at
// our terminal does not reach it.
func startDetached(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
