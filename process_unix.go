//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// signalGracefulStop asks the child to exit cleanly (snapshot on SIGTERM).
func signalGracefulStop(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
}
