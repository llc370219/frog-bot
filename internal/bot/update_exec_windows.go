//go:build windows

package bot

import (
	"os"
	"os/exec"
)

func reexecSelf() {
	if exe, err := os.Executable(); err == nil {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Start()
	}
	os.Exit(0)
}
