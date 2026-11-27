//go:build !windows

package clipool

import (
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"syscall"

	"frogbot/internal/sandbox"
)

// IsolationOptions controls Linux UID drop / bwrap / Landlock isolation for Coding CLI child processes.
type IsolationOptions struct {
	WorkerUser    string   // e.g. "frog-worker"
	ProtectedDirs []string // e.g. ["/data/host"]
	WorkspaceDir  string
	SelfBinary    string
	PreferBwrap   bool
}

// applyIsolation applies OS-level isolation and creates a dedicated process group so
// cancel_cli_task can reliably kill the entire child process tree.
func applyIsolation(cmd *exec.Cmd, opts IsolationOptions) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	if runtime.GOOS != "linux" {
		return nil
	}

	// 1. Prefer bubblewrap mount/PID namespace isolation if requested and installed
	if opts.PreferBwrap && opts.WorkspaceDir != "" {
		if bwrap, err := exec.LookPath("bwrap"); err == nil {
			bargs := []string{
				bwrap,
				"--ro-bind", "/", "/",
				"--dev", "/dev",
				"--proc", "/proc",
				"--tmpfs", "/tmp",
				"--bind", opts.WorkspaceDir, opts.WorkspaceDir,
				"--chdir", opts.WorkspaceDir,
				"--die-with-parent",
			}
			for _, prot := range opts.ProtectedDirs {
				if prot != "" {
					bargs = append(bargs, "--tmpfs", prot)
				}
			}
			bargs = append(bargs, "--", cmd.Path)
			bargs = append(bargs, cmd.Args[1:]...)
			cmd.Path = bwrap
			cmd.Args = bargs
			return nil
		}
	}

	// 2. Linux UID privilege drop to frog-worker when Host Daemon runs with CAP_SETUID / root
	if os.Geteuid() == 0 && opts.WorkerUser != "" {
		if u, err := user.Lookup(opts.WorkerUser); err == nil {
			uid, err1 := strconv.ParseUint(u.Uid, 10, 32)
			gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
			if err1 == nil && err2 == nil {
				cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
				return nil
			}
		}
	}

	// 3. Fallback to Linux Landlock protecting HostDir while keeping workspace writable
	if len(opts.ProtectedDirs) > 0 && opts.SelfBinary != "" && sandbox.Probe().Available {
		spec := sandbox.Spec{Protect: opts.ProtectedDirs}
		if opts.WorkspaceDir != "" {
			spec.Allow = append(spec.Allow, sandbox.Rule{Path: opts.WorkspaceDir, Access: sandbox.ReadWrite})
		}
		return sandbox.Wrap(cmd, opts.SelfBinary, spec)
	}
	return nil
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
