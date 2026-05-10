//go:build unix

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup arranges for cmd to run in its own process group so
// signals delivered to -pgid reach the entire subtree (the engine
// plus any tool grandchildren).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalProcessGroup sends sig to the process group whose leader has
// the given pid. Negating the pid is the syscall.Kill convention for
// "deliver to every member of the group."
func signalProcessGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
