//go:build linux

package sandbox

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

// Landlock 系统调用和常量（include/uapi/linux/landlock.h）。三个系统调用在所有架构上编号相同。
const (
	sysCreateRuleset = 444
	sysAddRule       = 445
	sysRestrictSelf  = 446

	createRulesetVersion = 1 << 0
	rulePathBeneath      = 1

	prSetNoNewPrivs = 38
	oPath           = 0x200000 // O_PATH（amd64 / arm64 相同）

)

// Probe 检查内核是否支持并启用了 Landlock。
func Probe() Status {
	abi, _, errno := syscall.Syscall(sysCreateRuleset, 0, 0, createRulesetVersion)
	if errno != 0 {
		reason := "内核不支持 Landlock（需要 Linux 5.13+ 并在 lsm= 里启用 landlock）"
		if errno == syscall.EOPNOTSUPP {
			reason = "内核编译了 Landlock 但没有启用（启动参数 lsm= 里加上 landlock）"
		}
		return Status{Reason: reason}
	}
	return Status{Available: true, ABI: int(abi)}
}

// enter 在当前进程启用沙箱；之后本进程和它的子进程都受限，无法撤销。
func enter(spec Spec) error {
	st := Probe()
	if !st.Available {
		return fmt.Errorf("%s", st.Reason)
	}
	spec, err := spec.Normalize()
	if err != nil {
		return err
	}
	handled := handledRights(st.ABI)
	attr := make([]byte, 8)
	binary.NativeEndian.PutUint64(attr, handled)
	fd, _, errno := syscall.Syscall(sysCreateRuleset, uintptr(unsafe.Pointer(&attr[0])), uintptr(len(attr)), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset：%v", errno)
	}
	ruleset := int(fd)
	defer syscall.Close(ruleset)
	for _, r := range plan(spec) {
		if err := addRule(ruleset, r.path, r.access&handled); err != nil {
			return err
		}
	}
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("prctl(NO_NEW_PRIVS)：%v", errno)
	}
	if _, _, errno := syscall.Syscall(sysRestrictSelf, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self：%v", errno)
	}
	return nil
}

func addRule(ruleset int, path string, access uint64) error {
	fd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil // 读不到的路径（权限、竞态删除）跳过：不开放就是最安全的结果
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		access &= fileRights
	}
	if access == 0 {
		return nil
	}
	// struct landlock_path_beneath_attr 是 packed：u64 allowed_access + s32 parent_fd，共 12 字节。
	var attr [12]byte
	binary.NativeEndian.PutUint64(attr[0:8], access)
	binary.NativeEndian.PutUint32(attr[8:12], uint32(int32(fd)))
	if _, _, errno := syscall.Syscall6(sysAddRule, uintptr(ruleset), rulePathBeneath, uintptr(unsafe.Pointer(&attr[0])), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_add_rule(%s)：%v", path, errno)
	}
	return nil
}

func execve(path string, argv, env []string) error { return syscall.Exec(path, argv, env) }

// SetNonDumpable 让当前进程不可被同一用户的其他进程 ptrace，也不能读它的 /proc/<pid>/mem、environ。
// 子进程 exec 之后会恢复成可调试，不影响 CLI。
func SetNonDumpable() error {
	const prSetDumpable = 4
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetDumpable, 0, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
