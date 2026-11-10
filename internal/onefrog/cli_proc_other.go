//go:build !windows

package onefrog

import "os/exec"

func setCLIProcessGroup(cmd *exec.Cmd) { setProcessGroup(cmd) }
