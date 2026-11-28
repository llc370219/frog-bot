//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
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

// enableVirtualTerminal 在 Windows 控制台开启 ANSI 转义序列（保证方块青蛙和终端二维码颜色正常显示）。
func enableVirtualTerminal() {
	const enableVT = 0x0004
	k32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := k32.NewProc("GetConsoleMode")
	setMode := k32.NewProc("SetConsoleMode")
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		h := f.Fd()
		var mode uint32
		r, _, _ := getMode.Call(h, uintptr(unsafe.Pointer(&mode)))
		if r != 0 && mode&enableVT == 0 {
			_, _, _ = setMode.Call(h, uintptr(mode|enableVT))
		}
	}
}
