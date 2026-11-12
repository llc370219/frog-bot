package onefrog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// RootTool 以 root 身份执行命令：每次都要用户在审批卡片上输入登录密码（或在授权窗口内点批准），
// 由独立的提权助手（frogbot rootd）执行。没有配置提权助手时 Agent 看不到这个工具。
const RootTool = "run_as_root"

// RootRunner 把命令交给提权助手执行。
type RootRunner interface {
	RootAvailable() bool
	RunAsRoot(ctx context.Context, command string, timeoutSec int, taskID string) (output string, exitCode int, err error)
}

var rootRunner struct {
	sync.RWMutex
	r RootRunner
}

// SetRootRunner 由主程序在提权可用时设置（nil 表示不可用）。
func SetRootRunner(r RootRunner) {
	rootRunner.Lock()
	defer rootRunner.Unlock()
	rootRunner.r = r
}

func currentRootRunner() RootRunner {
	rootRunner.RLock()
	defer rootRunner.RUnlock()
	return rootRunner.r
}

var rootTool = FuncTool{RootTool,
	"以 root 身份在这台服务器上执行命令（例如 apt-get install、修改 /etc 下的配置、管理 systemd 服务）。" +
		"只在普通 shell 确实因为权限不够做不了时使用。每次调用都会弹出审批卡片，用户输入登录密码后才执行；" +
		"reason 用一句话说明为什么需要 root。命令以非交互方式运行（DEBIAN_FRONTEND=noninteractive），" +
		"需要确认的程序请加 -y。timeout_sec 默认 600，最大 1800。",
	`{"type":"object","properties":{"command":{"type":"string"},"reason":{"type":"string"},"timeout_sec":{"type":"integer"}},"required":["command","reason"]}`,
	func(json.RawMessage) Risk { return RiskDanger },
	func(ctx context.Context, env ToolEnv, raw json.RawMessage) (string, error) {
		r := currentRootRunner()
		if r == nil {
			return "", errors.New("这台服务器没有开启提权")
		}
		a, err := parseArgs[struct {
			Command    string
			TimeoutSec int `json:"timeout_sec"`
		}](raw)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(a.Command) == "" {
			return "", errors.New("命令为空")
		}
		out, code, err := r.RunAsRoot(ctx, a.Command, a.TimeoutSec, env.TaskID)
		if err != nil {
			return out, err
		}
		return fmt.Sprintf("%s\n[exit code %d，以 root 执行]", out, code), nil
	}}
