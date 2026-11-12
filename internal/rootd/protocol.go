// Package rootd 是 frog 的提权助手：一个以 root 运行的小服务（frogbot rootd），只接受 frog 主进程
// 的连接，在用户输入登录密码授权后以 root 执行命令。
//
// 安全边界：
//   - 只认 frog 主进程：unix socket 只有 frogbot 用户能连，并且对端 PID 必须是 frog 服务的 MainPID。
//     Agent 的命令虽然也是 frogbot 用户，但 PID 不同；主进程设了不可调试，Agent 也无法借用它的连接。
//   - 密码自己核对：rootd 在 /etc/frogbot 下保存一份只有 root 能读的密码哈希，Agent 改了数据目录里的
//     auth.json 也没用。密码连续输错 5 次锁 15 分钟。
//   - 每条命令写审计日志（只有 root 能读）。
//
// 协议：每个连接发一行 JSON 请求，收一行 JSON 回复。
package rootd

import "time"

// DefaultSocket 是 rootd 的 unix socket（systemd 的 RuntimeDirectory=frogbot-rootd）。
const DefaultSocket = "/run/frogbot-rootd/rootd.sock"

// MaxGrant 是一次授权最长的免密窗口。
const MaxGrant = 30 * time.Minute

// Request 是 frog 发给 rootd 的请求。
type Request struct {
	Op string `json:"op"` // status / authorize / run / revoke / enable / disable / set_password

	Password string `json:"password,omitempty"`
	Minutes  int    `json:"minutes,omitempty"` // authorize：0 = 只放行下一条命令

	Command    string `json:"command,omitempty"`
	TimeoutSec int    `json:"timeout_sec,omitempty"`
	Dir        string `json:"dir,omitempty"`
	Task       string `json:"task,omitempty"`     // 审计用
	Approval   string `json:"approval,omitempty"` // 审计用

	Old string `json:"old,omitempty"` // set_password
	New string `json:"new,omitempty"`
}

// Status 是 rootd 当前的状态。
type Status struct {
	Configured  bool      `json:"configured"` // 有密码哈希
	Enabled     bool      `json:"enabled"`
	GrantUntil  time.Time `json:"grant_until,omitempty"`  // 免密窗口到期时间
	Pending     bool      `json:"pending,omitempty"`      // 有一条已授权、还没执行的命令
	LockedUntil time.Time `json:"locked_until,omitempty"` // 密码输错太多次
	// 剩余秒数（客户端直接用，不用解析时间）
	GrantLeftSec  int `json:"grant_left_sec,omitempty"`
	LockedLeftSec int `json:"locked_left_sec,omitempty"`
}

// Response 是 rootd 的回复。
type Response struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Status   Status `json:"status"`
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code"`
}
