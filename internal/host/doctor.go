package host

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"

	"frogbot/internal/agy"
	"frogbot/internal/sandbox"

	_ "modernc.org/sqlite"
)

// DoctorCheck represents a single environment diagnostic result.
type DoctorCheck struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"` // "ok" | "warn" | "error"
	Detail      string `json:"detail"`
	InstallCmd  string `json:"install_cmd,omitempty"`
	AutoFixable bool   `json:"auto_fixable,omitempty"`
}

// DoctorReport is produced by Host Doctor (decoupled from the chat loop).
type DoctorReport struct {
	Timestamp time.Time     `json:"timestamp"`
	OS        string        `json:"os"`
	Arch      string        `json:"arch"`
	GoVersion string        `json:"go_version"`
	Healthy   bool          `json:"healthy"`
	Checks    []DoctorCheck `json:"checks"`
}

// HostDoctor performs host-level environment self-checks and dependency installation
// guidance (repositioning the old built-in setup guide out of the conversation loop).
type HostDoctor struct {
	Layout Layout
	Config HostConfig
}

func NewHostDoctor(layout Layout, cfg HostConfig) *HostDoctor {
	return &HostDoctor{Layout: layout, Config: cfg}
}

func (d *HostDoctor) Diagnose(ctx context.Context) DoctorReport {
	rep := DoctorReport{
		Timestamp: time.Now(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		GoVersion: runtime.Version(),
		Healthy:   true,
	}
	add := func(c DoctorCheck) {
		if c.Status == "error" {
			rep.Healthy = false
		}
		rep.Checks = append(rep.Checks, c)
	}

	// 1. Host directory & permissions
	if st, err := os.Stat(d.Layout.HostDir); err != nil {
		add(DoctorCheck{ID: "host_dir", Name: "宿主数据目录", Status: "warn", Detail: fmt.Sprintf("尚未初始化: %s", d.Layout.HostDir), AutoFixable: true})
	} else {
		perm := st.Mode().Perm()
		if perm&0o077 != 0 {
			add(DoctorCheck{ID: "host_dir", Name: "宿主数据目录权限", Status: "warn", Detail: fmt.Sprintf("%s 权限为 %04o，建议收紧为 0700", d.Layout.HostDir, perm), AutoFixable: true})
		} else {
			add(DoctorCheck{ID: "host_dir", Name: "宿主数据目录权限", Status: "ok", Detail: fmt.Sprintf("%s (%04o)", d.Layout.HostDir, perm)})
		}
	}

	// 2. SQLite FTS5 trigram support check
	add(checkSQLiteTrigram(ctx))

	// 3. OS Isolation (Linux UID / bwrap / Landlock)
	add(d.checkIsolation())

	// 4. Official Coding CLIs (claude / codex / gemini / agy / pi)
	clis := agy.DiscoverAgents()
	var installed []string
	for _, c := range clis {
		if c.Installed {
			installed = append(installed, fmt.Sprintf("%s (%s)", c.ID, c.Path))
		}
	}
	if len(installed) == 0 {
		add(DoctorCheck{
			ID:         "coding_clis",
			Name:       "本机官方 Coding CLI 池",
			Status:     "warn",
			Detail:     "未检测到 claude / codex / gemini CLI（可按需安装）",
			InstallCmd: "npm i -g @anthropic-ai/claude-code @openai/codex @google/gemini-cli",
		})
	} else {
		add(DoctorCheck{
			ID:     "coding_clis",
			Name:   "本机官方 Coding CLI 池",
			Status: "ok",
			Detail: "已就绪: " + strings.Join(installed, ", "),
		})
	}

	// 5. Node.js & Playwright / Chromium
	add(checkBrowserRuntime())

	return rep
}

func checkSQLiteTrigram(ctx context.Context) DoctorCheck {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return DoctorCheck{ID: "sqlite_fts5", Name: "SQLite FTS5 (trigram)", Status: "error", Detail: err.Error()}
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE t USING fts5(body, tokenize='trigram'); INSERT INTO t VALUES ('中文全文检索测试');`); err != nil {
		return DoctorCheck{ID: "sqlite_fts5", Name: "SQLite FTS5 (trigram)", Status: "error", Detail: err.Error()}
	}
	var cnt int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM t WHERE t MATCH '全文检'`).Scan(&cnt); err != nil || cnt != 1 {
		return DoctorCheck{ID: "sqlite_fts5", Name: "SQLite FTS5 (trigram)", Status: "error", Detail: fmt.Sprintf("trigram 查询异常: cnt=%d err=%v", cnt, err)}
	}
	return DoctorCheck{ID: "sqlite_fts5", Name: "SQLite FTS5 (trigram)", Status: "ok", Detail: "纯 Go SQLite FTS5 中文 trigram 就绪"}
}

func (d *HostDoctor) checkIsolation() DoctorCheck {
	if runtime.GOOS != "linux" {
		return DoctorCheck{
			ID:     "isolation",
			Name:   "进程降权与沙箱隔离",
			Status: "warn",
			Detail: fmt.Sprintf("当前系统为 %s，Linux UID/bwrap/Landlock 隔离将在 Linux 服务器上生效", runtime.GOOS),
		}
	}
	var caps []string
	workerUser := d.Config.WorkerUser
	if workerUser == "" {
		workerUser = "frog-worker"
	}
	if _, err := user.Lookup(workerUser); err == nil {
		caps = append(caps, "UID("+workerUser+")")
	}
	if p, err := exec.LookPath("bwrap"); err == nil {
		caps = append(caps, "bwrap("+p+")")
	}
	if st := sandbox.Probe(); st.Available {
		caps = append(caps, fmt.Sprintf("Landlock(ABI=%d)", st.ABI))
	}
	if len(caps) == 0 {
		return DoctorCheck{
			ID:         "isolation",
			Name:       "进程降权与沙箱隔离",
			Status:     "warn",
			Detail:     "未检测到 frog-worker 用户、bwrap 或 Landlock，请创建低权限用户或安装 bubblewrap",
			InstallCmd: "sudo useradd -r -s /bin/bash frog-worker && sudo apt-get install -y bubblewrap",
		}
	}
	return DoctorCheck{ID: "isolation", Name: "进程降权与沙箱隔离", Status: "ok", Detail: strings.Join(caps, " + ")}
}

func checkBrowserRuntime() DoctorCheck {
	nodePath, nodeErr := exec.LookPath("node")
	var browserPath string
	for _, bin := range []string{"chromium", "chromium-browser", "google-chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if p, err := exec.LookPath(bin); err == nil {
			browserPath = p
			break
		}
		if st, err := os.Stat(bin); err == nil && !st.IsDir() {
			browserPath = bin
			break
		}
	}
	switch {
	case nodeErr == nil && browserPath != "":
		return DoctorCheck{ID: "playwright", Name: "Playwright / 浏览器器官", Status: "ok", Detail: fmt.Sprintf("Node=%s, Browser=%s", nodePath, browserPath)}
	case nodeErr == nil:
		return DoctorCheck{
			ID:         "playwright",
			Name:       "Playwright / 浏览器器官",
			Status:     "warn",
			Detail:     "已检测到 Node.js，但未找到系统 Chromium（可运行 playwright install chromium）",
			InstallCmd: "npx playwright install chromium",
		}
	default:
		return DoctorCheck{
			ID:         "playwright",
			Name:       "Playwright / 浏览器器官",
			Status:     "warn",
			Detail:     "未检测到 Node.js 或 Chromium，24h 浏览器打卡器官暂不可用",
			InstallCmd: "curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash - && sudo apt-get install -y nodejs && npx playwright install --with-deps chromium",
		}
	}
}

// FixPermissions repairs host directory permissions (0700).
func (d *HostDoctor) FixPermissions() error {
	return d.Layout.EnsureHostDirs()
}
