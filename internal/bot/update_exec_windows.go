//go:build windows

package bot

import (
	"os"
	"os/exec"
)

func reexecSelf(exePath string) {
	if exePath != "" {
		cmd := exec.Command(exePath, os.Args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Start()
	}
	os.Exit(0)
}
