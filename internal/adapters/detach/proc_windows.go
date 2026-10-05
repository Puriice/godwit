//go:build windows

package detach

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
	createBreakawayJob    = 0x01000000
)

// startDetached starts cmd with no console and outside our process group. It
// also tries to leave our job object, which a terminal may use to kill
// everything it started when it closes; that is refused when the job does not
// allow breakaway, so it falls back to staying in it.
func startDetached(cmd *exec.Cmd) error {
	flags := uint32(detachedProcess | createNewProcessGroup)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | breakawayFlag, HideWindow: true}
	if err := cmd.Start(); err == nil {
		return nil
	}
	// A Cmd whose Start failed cannot be started again ("exec: already
	// started"), so the fallback is a copy of it.
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.Dir, retry.Env = cmd.Dir, cmd.Env
	retry.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
	if err := retry.Start(); err != nil {
		return err
	}
	cmd.Process = retry.Process
	return nil
}

// breakawayFlag asks to leave our job object. A variable so a test can make
// the first attempt fail.
var breakawayFlag uint32 = createBreakawayJob

// alive reports whether a process with this pid exists. On Windows
// FindProcess fails for a pid that is gone.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release() //nolint:errcheck
	return true
}
