//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureDetachedCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func terminateProcess(pid int, force bool) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if force {
		return proc.Signal(syscall.SIGKILL)
	}
	return proc.Signal(syscall.SIGTERM)
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
