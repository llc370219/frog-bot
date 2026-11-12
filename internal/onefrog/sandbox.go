package onefrog

import (
	"errors"
	"os/exec"
	"sync"

	"frogbot/internal/sandbox"
)

// Agent 启动的进程（shell 命令、外部 CLI、一键安装时的 npm）都经过这里进沙箱。
// 没有调用 SetSandbox（本机开发、非 Linux、内核不支持）时不套沙箱。

var sandboxCfg struct {
	sync.RWMutex
	self string
	spec sandbox.Spec
	on   bool
}

// SetSandbox 由主程序在启动时调用：self 是 frogbot 可执行文件，spec 是受保护目录和例外。
func SetSandbox(self string, spec sandbox.Spec) {
	sandboxCfg.Lock()
	defer sandboxCfg.Unlock()
	sandboxCfg.self, sandboxCfg.spec, sandboxCfg.on = self, spec, self != ""
}

// SandboxEnabled 说明 Agent 的命令现在是否在沙箱里运行（提权功能依赖它）。
func SandboxEnabled() bool {
	sandboxCfg.RLock()
	defer sandboxCfg.RUnlock()
	return sandboxCfg.on
}

// sandboxed 把 cmd 改写成在沙箱里运行；extra 是这次额外开放的路径（例如安装时可写的 tools 目录）。
func sandboxed(cmd *exec.Cmd, extra ...sandbox.Rule) error {
	sandboxCfg.RLock()
	self, spec, on := sandboxCfg.self, sandboxCfg.spec, sandboxCfg.on
	sandboxCfg.RUnlock()
	if !on {
		return nil
	}
	if cmd.Path == "" {
		return errors.New("沙箱：命令为空")
	}
	spec.Allow = append(append([]sandbox.Rule(nil), spec.Allow...), extra...)
	return sandbox.Wrap(cmd, self, spec)
}
