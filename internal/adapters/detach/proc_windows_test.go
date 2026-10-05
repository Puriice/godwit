//go:build windows

package detach

import (
	"os/exec"
	"testing"
)

// A job object that forbids breakaway makes the first start fail, as on
// GitHub's Windows runners. The start must then succeed without the flag.
func TestStartDetachedFallsBackWhenBreakawayIsRefused(t *testing.T) {
	// DETACHED_PROCESS cannot be combined with CREATE_NEW_CONSOLE, so adding
	// the latter makes CreateProcess refuse the first attempt.
	const createNewConsole = 0x00000010
	old := breakawayFlag
	breakawayFlag = createNewConsole
	t.Cleanup(func() { breakawayFlag = old })

	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := startDetached(cmd); err != nil {
		t.Fatalf("startDetached = %v, want the fallback to succeed", err)
	}
	if cmd.Process == nil || cmd.Process.Pid == 0 {
		t.Fatalf("no process recorded: %+v", cmd.Process)
	}
	cmd.Process.Release() //nolint:errcheck // never waited on, like a real worker
}
