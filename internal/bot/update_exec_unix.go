//go:build !windows

package bot

import (
	"os"
	"syscall"
)

func reexecSelf(exePath string) {
	if exePath != "" {
		_ = syscall.Exec(exePath, os.Args, os.Environ())
	}
	os.Exit(0)
}
