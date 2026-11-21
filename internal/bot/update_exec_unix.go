//go:build !windows

package bot

import (
	"os"
	"syscall"
)

func reexecSelf() {
	exe, err := os.Executable()
	if err == nil {
		_ = syscall.Exec(exe, os.Args, os.Environ())
	}
	os.Exit(0)
}
