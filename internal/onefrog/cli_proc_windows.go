package onefrog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

func setCLIProcessGroup(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.Cancel = func() error {
		// Kill the tree (including the native child of an npm CLI) before the
		// root exits. Fall back to terminating the root if taskkill is unavailable.
		kill := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe"),
			"/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
		if err := kill.Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
