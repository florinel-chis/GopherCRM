//go:build !unix

package service

import "os/exec"

// configureQuotePDFProcessGroup has no process group to set up on this
// platform: the context's cancellation kills the browser process itself.
func configureQuotePDFProcessGroup(*exec.Cmd) {}

// killQuotePDFProcessGroup kills the browser process.
func killQuotePDFProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
