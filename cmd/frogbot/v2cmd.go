package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"frogbot/internal/host"
	"frogbot/internal/ops"
	"frogbot/internal/remote"
	"frogbot/internal/security"
)

func v2RelayWSMain(args []string) {
	fs := flag.NewFlagSet("relay-ws", flag.ExitOnError)
	listen := fs.String("listen", env(":18443", "RELAY_LISTEN"), "监听地址（建议挂在 Caddy/Nginx 443 反代后）")
	token := fs.String("token", env("", "RELAY_TOKEN", "FROGBOT_RELAY_TOKEN"), "可选访问令牌（公网开箱即用中继留空即可）")
	maxConnsPerIP := fs.Int("max-conns-per-ip", 64, "单 IP 最大并发连接数")
	maxMBPerMin := fs.Int64("max-mb-per-min", 128, "单连接每分钟最大转发流量（MB）")
	_ = fs.Parse(args)

	limits := remote.DefaultWSSRelayLimits()
	if *maxConnsPerIP > 0 {
		limits.MaxConnsPerIP = *maxConnsPerIP
	}
	if *maxMBPerMin > 0 {
		limits.MaxBytesPerMin = *maxMBPerMin << 20
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv, err := remote.StartWSRelayServerWithLimits(ctx, *listen, *token, limits)
	if err != nil {
		fatal("启动 E2EE WSS 盲中继失败: %v", err)
	}
	fmt.Printf("🐸 Frog Bot E2EE WSS 盲中继已启动：%s/ws（零知识密文转发，单IP上限=%d，单连接限速=%dMB/min）\n",
		*listen, limits.MaxConnsPerIP, limits.MaxBytesPerMin>>20)
	if err := srv.ListenAndServe(); err != nil && err.Error() != "http: Server closed" {
		log.Fatalf("relay-ws: %v", err)
	}
}

func v2DoctorMain(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dataDir := fs.String("data", env("", "DATA_DIR"), "数据目录")
	fix := fs.Bool("fix", false, "自动修复目录权限")
	_ = fs.Parse(args)

	d, _ := dirs(*dataDir)
	layout := host.NewLayout(d)
	if *fix {
		_ = layout.EnsureHostDirs()
	}
	cfg, _ := host.LoadHostConfig(layout.HostConfigPath())
	rep := host.NewHostDoctor(layout, cfg).Diagnose(context.Background())
	fmt.Printf("Frog Bot Host Doctor (%s/%s, %s)\n", rep.OS, rep.Arch, rep.GoVersion)
	for _, c := range rep.Checks {
		icon := "✓"
		if c.Status == "warn" {
			icon = "!"
		} else if c.Status == "error" {
			icon = "✗"
		}
		fmt.Printf("  [%s] %s: %s\n", icon, c.Name, c.Detail)
		if c.InstallCmd != "" {
			fmt.Printf("      建议命令: %s\n", c.InstallCmd)
		}
	}
}

func v2BackupMain(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data", env("", "DATA_DIR"), "数据目录")
	out := fs.String("out", fmt.Sprintf("frogbot-backup-%s.frogbak", time.Now().Format("20060102-150405")), "输出路径")
	pass := fs.String("pass", os.Getenv("FROGBOT_BACKUP_PASS"), "加密口令")
	_ = fs.Parse(args)
	d, _ := dirs(*dataDir)
	man, err := ops.Backup(d, *out, *pass)
	if err != nil {
		fatal("备份失败: %v", err)
	}
	fmt.Printf("已加密导出 %d 个文件到 %s（%.1f KB）\n", man.Files, *out, float64(man.CipherBytes)/1024)
}

func v2RestoreMain(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	in := fs.String("in", "", "备份文件路径")
	dataDir := fs.String("data", env("", "DATA_DIR"), "目标恢复目录")
	pass := fs.String("pass", os.Getenv("FROGBOT_BACKUP_PASS"), "解密口令")
	_ = fs.Parse(args)
	d, _ := dirs(*dataDir)
	man, err := ops.Restore(*in, d, *pass)
	if err != nil {
		fatal("恢复失败: %v", err)
	}
	fmt.Printf("已从 %s 恢复 %d 个文件到 %s\n", *in, man.Files, d)
}

func v2GCMain(args []string) {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	dataDir := fs.String("data", env("", "DATA_DIR"), "数据目录")
	days := fs.Int("days", 7, "保留天数")
	maxMB := fs.Int64("max-mb", 500, "最大日志容量 MB")
	_ = fs.Parse(args)
	d, ws := dirs(*dataDir)
	rep := ops.NewDiskGC(d, ops.GCConfig{LogMaxAge: time.Duration(*days) * 24 * time.Hour, MaxLogBytes: *maxMB << 20}, ws).RunNow()
	fmt.Printf("GC 完成：清理过期指针 %d、过期日志 %d、超额日志 %d，回收 %.1f KB\n",
		rep.PointersRemoved, rep.LogsRemovedByAge, rep.LogsRemovedByCap, float64(rep.BytesReclaimed)/1024)
}

func v2ExecMain(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	sock := fs.String("sock", env("/data/host/frog-exec.sock", "FROG_EXEC_SOCK"), "frog-exec socket")
	botID := fs.String("bot", env("main", "FROG_BOT_ID"), "Bot ID")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		fatal("用法: frogbot exec [--sock ...] [--bot main] -- <cmd> [args...]")
	}
	cwd, _ := os.Getwd()
	ev := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.Contains(v, "{{secret:") {
			ev[k] = v
		}
	}
	resp, err := security.RunExecClient(*sock, security.ExecRequest{
		BotID: *botID, Cwd: cwd, Command: rest[0], Args: rest[1:], Env: ev, TriggerSource: "cli:frogbot-exec",
	})
	if err != nil {
		fatal("%v", err)
	}
	_, _ = os.Stdout.WriteString(resp.Stdout)
	_, _ = os.Stderr.WriteString(resp.Stderr)
	os.Exit(resp.ExitCode)
}
