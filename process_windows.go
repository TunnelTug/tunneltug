//go:build windows

package main

import (
	"os/exec"
)

// signalGracefulStop is a best-effort stop on Windows (no SIGTERM to children).
func signalGracefulStop(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Immediate kill; prefer Linux/k3s for shutdown snapshots.
	_ = cmd.Process.Kill()
}
