//go:build linux

package main

import (
	"os/exec"
	"strings"
)

// execOutput is for subcommands that run as their own process (not PID 1).
func execOutput(argv []string) (string, error) {
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
