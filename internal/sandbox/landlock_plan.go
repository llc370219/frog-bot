package sandbox

import (
	"os"
	"path/filepath"
)

// Landlock 的文件系统权限位（include/uapi/linux/landlock.h）和规则规划。规划是纯 Go，可以在任何系统上测试。
const (
	accessExecute    = 1 << 0
	accessWriteFile  = 1 << 1
	accessReadFile   = 1 << 2
	accessReadDir    = 1 << 3
	accessRemoveDir  = 1 << 4
	accessRemoveFile = 1 << 5
	accessMakeChar   = 1 << 6
	accessMakeDir    = 1 << 7
	accessMakeReg    = 1 << 8
	accessMakeSock   = 1 << 9
	accessMakeFifo   = 1 << 10
	accessMakeBlock  = 1 << 11
	accessMakeSym    = 1 << 12
	accessRefer      = 1 << 13 // ABI 2
	accessTruncate   = 1 << 14 // ABI 3
	accessIoctlDev   = 1 << 15 // ABI 5

	// 只对普通文件有意义的权限；给文件加规则时其他位会被内核拒绝。
	fileRights = accessExecute | accessWriteFile | accessReadFile | accessTruncate | accessIoctlDev
	readRights = accessExecute | accessReadFile | accessReadDir
	// 受保护目录的上级目录（例如家目录）：能列出名字、能新建文件和目录，但不能读写、删除、建链接。
	// 这些权限会延伸到受保护目录里，所以最坏情况只是在里面多出空文件，读不到也改不了已有内容。
	ancestorRights = accessReadDir | accessMakeReg | accessMakeDir
)

func handledRights(abi int) uint64 {
	r := uint64(1<<13 - 1) // ABI 1：EXECUTE … MAKE_SYM
	if abi >= 2 {
		r |= accessRefer
	}
	if abi >= 3 {
		r |= accessTruncate
	}
	if abi >= 5 {
		r |= accessIoctlDev
	}
	return r
}

type planned struct {
	path   string
	access uint64
}

// plan 算出要加的规则：从根目录一路往受保护目录走，沿途每一层的「旁支」整棵开放，
// 上级目录本身只给 ancestorRights，受保护目录里只开放 spec.Allow。
func plan(spec Spec) []planned { return planFrom("/", spec) }

// planFrom 和 plan 一样，只是从 root 开始走（测试用）。
func planFrom(root string, spec Spec) []planned {
	all := uint64(1<<16 - 1)
	var out []planned
	ancestorOf := func(p string) bool { // p 是某个受保护目录的上级或就是它
		for _, d := range spec.Protect {
			if within(d, p) {
				return true
			}
		}
		return false
	}
	protected := func(p string) bool {
		for _, d := range spec.Protect {
			if within(p, d) {
				return true
			}
		}
		return false
	}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if ancestorOf(p) {
				if !protected(p) {
					out = append(out, planned{p, ancestorRights})
					walk(p)
				}
				continue
			}
			if e.Type()&os.ModeSymlink != 0 {
				// 链接按目标生效：指向受保护目录或它的上级（例如 /data -> /var/lib/frogbot/data）的不开放。
				target, err := filepath.EvalSymlinks(p)
				if err != nil || protected(target) || ancestorOf(target) {
					continue
				}
			}
			out = append(out, planned{p, all})
		}
	}
	out = append(out, planned{root, accessReadDir})
	walk(root)
	for _, r := range spec.Allow {
		access := uint64(readRights)
		if r.Access == ReadWrite {
			access = all
		}
		out = append(out, planned{r.Path, access})
	}
	return out
}
