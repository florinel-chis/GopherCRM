//go:build unix

package service

import (
	"os/exec"
	"syscall"
)

// configureQuotePDFProcessGroup starts the browser as the leader of a new
// process group and makes the context's cancellation kill that whole group:
// Chrome's helper processes, and anything a launcher script started, die with
// it.
func configureQuotePDFProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killQuotePDFProcessGroup(cmd)
		return nil
	}
}

// killQuotePDFProcessGroup kills every process left in the browser's group.
// A group that is already empty is not an error.
func killQuotePDFProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
