//go:build windows

package clipool

import "os/exec"

type IsolationOptions struct {
	WorkerUser    string
	ProtectedDirs []string
	WorkspaceDir  string
	SelfBinary    string
	PreferBwrap   bool
}

func applyIsolation(_ *exec.Cmd, _ IsolationOptions) error { return nil }

func killProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
