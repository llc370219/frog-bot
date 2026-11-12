package bot

import (
	"context"
	"errors"
	"sync"
	"time"

	"frogbot/internal/onefrog"
	"frogbot/internal/rootd"
	"frogbot/internal/sandbox"
)

// Privilege 是 frog 主进程这一侧的提权控制：只有服务器模式（有登录）+ Agent 沙箱已启用 + 装了提权助手时才可用。
// 沙箱是前提：没有沙箱时 Agent 的命令能读到会话签名密钥，伪造登录后自己批准自己。
type Privilege struct {
	Client  *rootd.Client
	Sandbox sandbox.Status
	Server  bool
}

var privilege struct {
	sync.RWMutex
	p *Privilege
}

// SetPrivilege 由主程序在启动时调用。
func SetPrivilege(p *Privilege) {
	privilege.Lock()
	privilege.p = p
	privilege.Unlock()
	if p != nil {
		onefrog.SetRootRunner(p)
	} else {
		onefrog.SetRootRunner(nil)
	}
}

func currentPrivilege() *Privilege {
	privilege.RLock()
	defer privilege.RUnlock()
	return privilege.p
}

// unavailable 返回提权不能用的原因；能用时返回空。
func (p *Privilege) unavailable() string {
	switch {
	case p == nil || !p.Server:
		return "只有服务器模式（需要登录）才能提权"
	case !p.Sandbox.Available || !onefrog.SandboxEnabled():
		reason := p.Sandbox.Reason
		if reason == "" {
			reason = "沙箱没有通过自检"
		}
		return "Agent 沙箱不可用（" + reason + "），为了安全不开放提权"
	case !p.Client.Installed():
		return "这台服务器没有安装提权助手：重新运行一次安装命令即可"
	}
	return ""
}

// RootAvailable 让 Agent 只在提权真正可用时看到 run_as_root 工具。
func (p *Privilege) RootAvailable() bool {
	if p.unavailable() != "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := p.Client.Do(ctx, rootd.Request{Op: "status"})
	return err == nil && resp.Status.Configured && resp.Status.Enabled
}

// RunAsRoot 实现 onefrog.RootRunner：用户已经在审批卡片上授权，rootd 会用掉这次授权。
func (p *Privilege) RunAsRoot(ctx context.Context, command string, timeoutSec int, taskID string) (string, int, error) {
	if why := p.unavailable(); why != "" {
		return "", -1, errors.New(why)
	}
	resp, err := p.Client.Do(ctx, rootd.Request{Op: "run", Command: command, TimeoutSec: timeoutSec, Task: taskID})
	return resp.Output, resp.ExitCode, err
}

// authorize 在用户批准 root 命令时调用：password 为空时要求免密窗口还开着。
func (p *Privilege) authorize(password string, minutes int) error {
	if why := p.unavailable(); why != "" {
		return errors.New(why)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := p.Client.Do(ctx, rootd.Request{Op: "authorize", Password: password, Minutes: minutes})
	return err
}

// privilegeView 是 GET /api/settings/privilege 的结果。
type privilegeView struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
	Sandbox   sandbox.Status `json:"sandbox"`
	Status    *rootd.Status  `json:"status,omitempty"`
	Error     string         `json:"error,omitempty"`
}

func (p *Privilege) view(ctx context.Context) privilegeView {
	if p == nil {
		return privilegeView{Reason: "只有服务器模式（需要登录）才能提权"}
	}
	v := privilegeView{Sandbox: p.Sandbox, Reason: p.unavailable()}
	if v.Reason != "" {
		return v
	}
	resp, err := p.Client.Do(ctx, rootd.Request{Op: "status"})
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Status = &resp.Status
	switch {
	case !resp.Status.Configured:
		v.Reason = "提权助手还没有设置密码：重新运行一次安装命令即可"
	case !resp.Status.Enabled:
		v.Reason = "提权已关闭"
	default:
		v.Available = true
	}
	return v
}

func (p *Privilege) action(ctx context.Context, action, password string) error {
	if why := p.unavailable(); why != "" {
		return errors.New(why)
	}
	var req rootd.Request
	switch action {
	case "enable":
		req = rootd.Request{Op: "enable", Password: password}
	case "disable":
		req = rootd.Request{Op: "disable"}
	case "revoke":
		req = rootd.Request{Op: "revoke"}
	default:
		return errors.New("未知的操作")
	}
	_, err := p.Client.Do(ctx, req)
	return err
}

// SyncPassword 在网页上改登录密码时同步给提权助手（没装或没配置时忽略）。
func SyncPassword(old, next string) error {
	p := currentPrivilege()
	if p == nil || !p.Client.Installed() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := p.Client.Do(ctx, rootd.Request{Op: "status"})
	if err != nil || !resp.Status.Configured {
		return nil
	}
	if _, err := p.Client.Do(ctx, rootd.Request{Op: "set_password", Old: old, New: next}); err != nil {
		return errors.New("同步提权助手的密码失败：" + err.Error())
	}
	return nil
}
