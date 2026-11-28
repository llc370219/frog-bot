//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	winDetachedProcess       = 0x00000008
	winCreateNewProcessGroup = 0x00000200
	winCreateNoWindow        = 0x08000000
)

func configureDetachedCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: winCreateNewProcessGroup | winDetachedProcess | winCreateNoWindow,
		HideWindow:    true,
	}
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

func terminateProcess(pid int, force bool) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
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
