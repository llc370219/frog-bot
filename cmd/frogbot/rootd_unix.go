//go:build unix

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"frogbot/internal/auth"
	"frogbot/internal/rootd"
)

const (
	defaultRootdState = "/etc/frogbot/rootd.json"
	defaultRootdAudit = "/var/log/frogbot-rootd.log"
)

// rootdMain：
//
//	frogbot rootd [-socket …] [-unit frogbot.service] [-user frogbot]   以 root 运行的提权助手
//	frogbot rootd sync-password [-data 目录] [-enable]                   把登录密码哈希同步给提权助手（root）
func rootdMain(args []string) {
	if len(args) > 0 && args[0] == "sync-password" {
		rootdSync(args[1:])
		return
	}
	fl := flag.NewFlagSet("rootd", flag.ExitOnError)
	socket := fl.String("socket", rootd.DefaultSocket, "unix socket 路径")
	statePath := fl.String("state", defaultRootdState, "状态文件（密码哈希、开关），root 0600")
	auditPath := fl.String("audit", defaultRootdAudit, "审计日志")
	unit := fl.String("unit", "frogbot.service", "frog 的 systemd 服务名：只接受它的 MainPID")
	pidFile := fl.String("pid-file", "", "不用 systemd 时：从这个文件读 frog 主进程 PID（文件必须属于 root）")
	svcUser := fl.String("user", "frogbot", "frog 服务运行的系统用户")
	_ = fl.Parse(args)
	log.SetFlags(log.Ldate | log.Ltime)
	if os.Geteuid() != 0 {
		fatal("frogbot rootd 必须以 root 运行")
	}
	u, err := user.Lookup(*svcUser)
	if err != nil {
		fatal("找不到用户 %s：%v", *svcUser, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	srv := &rootd.Server{StatePath: *statePath, AuditPath: *auditPath}
	if err := srv.Load(); err != nil {
		fatal("读取 %s 失败：%v", *statePath, err)
	}
	srv.Verify = func(pid, peerUID int) error {
		if peerUID != uid {
			return fmt.Errorf("不是 %s 用户", *svcUser)
		}
		main, err := mainPID(*unit, *pidFile)
		if err != nil {
			return err
		}
		if pid != main {
			return fmt.Errorf("不是 frog 主进程（PID %d，主进程 %d）", pid, main)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(*socket), 0o755); err != nil {
		fatal("%v", err)
	}
	_ = os.Remove(*socket)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: *socket, Net: "unix"})
	if err != nil {
		fatal("监听 %s 失败：%v", *socket, err)
	}
	ln.SetUnlinkOnClose(true)
	// 只有 frog 的服务用户能连（再加上 PID 检查）
	if err := os.Chown(*socket, uid, gid); err != nil {
		fatal("%v", err)
	}
	if err := os.Chmod(*socket, 0o600); err != nil {
		fatal("%v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("frog 提权助手已启动：%s（只接受 %s 的主进程）", *socket, *unit)
	if err := srv.Serve(ctx, ln); err != nil {
		fatal("%v", err)
	}
}

// mainPID 查 frog 主进程的 PID：优先 systemd 的 MainPID，否则读 root 拥有的 PID 文件。
func mainPID(unit, pidFile string) (int, error) {
	if pidFile != "" {
		st, err := os.Stat(pidFile)
		if err != nil {
			return 0, err
		}
		if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != 0 || st.Mode().Perm()&0o022 != 0 {
			return 0, errors.New("PID 文件必须属于 root 且别人不能写")
		}
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	out, err := exec.Command("systemctl", "show", "--property=MainPID", "--value", unit).Output()
	if err != nil {
		return 0, fmt.Errorf("查询 %s 失败：%v", unit, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("%s 没有在运行", unit)
	}
	return pid, nil
}

func rootdSync(args []string) {
	fl := flag.NewFlagSet("rootd sync-password", flag.ExitOnError)
	data := fl.String("data", "", "frog 数据目录（默认同主程序）")
	statePath := fl.String("state", defaultRootdState, "提权助手的状态文件")
	enable := fl.Bool("enable", false, "同时开启提权")
	_ = fl.Parse(args)
	if os.Geteuid() != 0 {
		fatal("需要 root 运行")
	}
	dataDir, _ := dirs(*data)
	hash, err := auth.ReadHash(dataDir)
	if err != nil {
		fatal("读取登录密码失败：%v", err)
	}
	srv := &rootd.Server{StatePath: *statePath}
	if err := srv.Load(); err != nil {
		fatal("%v", err)
	}
	if err := srv.SetHash(hash, *enable); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("已把登录密码同步给提权助手（%s）。正在运行的提权助手需要重启：systemctl restart frogbot-rootd\n", *statePath)
}

// syncRootdAfterReset：root 运行 reset-password 时顺便同步给提权助手。
func syncRootdAfterReset(dataDir string) {
	if os.Geteuid() != 0 {
		return
	}
	if _, err := os.Stat(defaultRootdState); err != nil {
		return
	}
	hash, err := auth.ReadHash(dataDir)
	if err != nil {
		return
	}
	srv := &rootd.Server{StatePath: defaultRootdState}
	if srv.Load() == nil && srv.SetHash(hash, false) == nil {
		_ = exec.Command("systemctl", "try-restart", "frogbot-rootd.service").Run()
		fmt.Println("提权助手的密码也已同步。")
	}
}
