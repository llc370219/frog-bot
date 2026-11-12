//go:build linux

package rootd

import (
	"net"
	"syscall"
)

// peerCred 读取 unix socket 对端进程的 PID 和 UID（SO_PEERCRED，由内核提供，无法伪造）。
func peerCred(c *net.UnixConn) (pid, uid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, 0, err
	}
	if serr != nil {
		return 0, 0, serr
	}
	return int(cred.Pid), int(cred.Uid), nil
}
