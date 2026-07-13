package main

import (
	"fmt"
	"runtime"
)

var (
	Version   = "0.1.0"
	Commit    = "dev"
	BuildDate = "unknown"
)

func versionString() string {
	return fmt.Sprintf("tunneltug %s (commit %s, built %s, %s/%s)",
		Version, Commit, BuildDate, runtime.GOOS, runtime.GOARCH)
}
