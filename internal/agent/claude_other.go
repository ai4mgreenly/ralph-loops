//go:build !unix

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup is a no-op on non-unix platforms. The unix-only
// SysProcAttr.Setpgid field has no equivalent on Windows, so the
// process-group plumbing simply isn't wired up there; cancellation
// falls back to signalling the immediate child only.
func setProcessGroup(cmd *exec.Cmd) {}

// signalProcessGroup is a no-op stub on non-unix platforms. There is
// no portable way to signal a process group; callers fall back to the
// default exec.Cmd cancellation path (kill of the leader).
func signalProcessGroup(pid int, sig syscall.Signal) error {
	return nil
}
