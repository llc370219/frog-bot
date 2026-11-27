// Command frogctl is the Frog Bot V2 host administration & migration CLI:
//   - frogctl doctor   : Run Host Doctor diagnostics and fix directory permissions
//   - frogctl backup   : Encrypt & export all bots, vault, skills, history.db, and browser profiles
//   - frogctl restore  : Decrypt & resurrect all assistants on a new server
//   - frogctl gc       : Trigger immediate disk garbage collection (tmp_* pointers, old CLI logs)
//   - frogctl pair     : Issue an Ed25519 FrogLink Mesh pairing code
//   - frogctl status   : Inspect host node identity, peers, and bots
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"frogbot/internal/host"
	"frogbot/internal/mesh"
	"frogbot/internal/ops"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "doctor":
		runDoctor(args)
	case "backup":
		runBackup(args)
	case "restore":
		runRestore(args)
	case "gc":
		runGC(args)
	case "pair":
		runPair(args)
	case "status":
		runStatus(args)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`frogctl — Frog Bot V2 宿主管理与一键迁移工具

用法:
  frogctl doctor  [-data ./data] [-fix]
  frogctl backup  [-data ./data] -out backup.frogbak -pass <口令>
  frogctl restore -in backup.frogbak -data ./data -pass <口令>
  frogctl gc      [-data ./data] [-days 7] [-max-mb 500]
  frogctl pair    [-data ./data] [-public-url https://...] [-tag "[家庭宽带IP]"]
  frogctl status  [-data ./data]`)
}

func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dataDir := fs.String("data", envOr("DATA_DIR", "./data"), "数据根目录")
	fix := fs.Bool("fix", false, "自动修复宿主目录权限")
	asJSON := fs.Bool("json", false, "输出 JSON")
	_ = fs.Parse(args)

	layout := host.NewLayout(*dataDir)
	if *fix {
		if err := layout.EnsureHostDirs(); err != nil {
			fatalf("修复目录失败: %v", err)
		}
	}
	cfg, _ := host.LoadHostConfig(layout.HostConfigPath())
	doc := host.NewHostDoctor(layout, cfg)
	rep := doc.Diagnose(context.Background())
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return
	}
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

func runBackup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data", envOr("DATA_DIR", "./data"), "数据根目录")
	outPath := fs.String("out", fmt.Sprintf("frogbot-backup-%s.frogbak", time.Now().Format("20060102-150405")), "输出加密备份文件路径")
	pass := fs.String("pass", os.Getenv("FROGBOT_BACKUP_PASS"), "加密口令（至少 6 位）")
	_ = fs.Parse(args)

	if *pass == "" {
		fatalf("请通过 -pass 或环境变量 FROGBOT_BACKUP_PASS 提供加密口令")
	}
	man, err := ops.Backup(*dataDir, *outPath, *pass)
	if err != nil {
		fatalf("备份失败: %v", err)
	}
	fmt.Printf("备份完成: %s（共 %d 个文件，原始 %.1f KB -> 加密 %.1f KB）\n",
		*outPath, man.Files, float64(man.PlainBytes)/1024, float64(man.CipherBytes)/1024)
}

func runRestore(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	inPath := fs.String("in", "", "加密备份文件路径 (.frogbak)")
	dataDir := fs.String("data", envOr("DATA_DIR", "./data"), "目标恢复数据目录")
	pass := fs.String("pass", os.Getenv("FROGBOT_BACKUP_PASS"), "解密口令")
	_ = fs.Parse(args)

	if *inPath == "" || *pass == "" {
		fatalf("用法: frogctl restore -in backup.frogbak -data ./data -pass <口令>")
	}
	man, err := ops.Restore(*inPath, *dataDir, *pass)
	if err != nil {
		fatalf("恢复失败: %v", err)
	}
	fmt.Printf("恢复完成: 已还原 %d 个文件（%.1f KB）到 %s\n", man.Files, float64(man.PlainBytes)/1024, *dataDir)
}

func runGC(args []string) {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	dataDir := fs.String("data", envOr("DATA_DIR", "./data"), "数据根目录")
	days := fs.Int("days", 7, "CLI 日志与截图保留天数")
	maxMB := fs.Int64("max-mb", 500, "日志与截图最大容量 (MB)")
	_ = fs.Parse(args)

	gc := ops.NewDiskGC(*dataDir, ops.GCConfig{
		LogMaxAge:   time.Duration(*days) * 24 * time.Hour,
		MaxLogBytes: *maxMB << 20,
	})
	rep := gc.RunNow()
	fmt.Printf("磁盘 GC 完成: 清理过期指针 %d 个、过期日志 %d 个、超额日志 %d 个，共回收 %.1f KB（剩余日志 %.1f KB）\n",
		rep.PointersRemoved, rep.LogsRemovedByAge, rep.LogsRemovedByCap,
		float64(rep.BytesReclaimed)/1024, float64(rep.RemainingLogBytes)/1024)
}

func runPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dataDir := fs.String("data", envOr("DATA_DIR", "./data"), "数据根目录")
	pubURL := fs.String("public-url", "", "本机公网地址（公网节点填写）")
	tag := fs.String("tag", "", "静态网络标签（如 [家庭宽带IP]）")
	_ = fs.Parse(args)

	layout := host.NewLayout(*dataDir)
	_ = layout.EnsureHostDirs()
	cfg, _ := host.LoadHostConfig(layout.HostConfigPath())
	if *tag != "" {
		cfg.NetworkTag = *tag
		_ = host.SaveHostConfig(layout.HostConfigPath(), cfg)
	}
	m, err := mesh.OpenMesh(layout.HostDir, cfg.NodeName, cfg.NetworkTag, *pubURL)
	if err != nil {
		fatalf("初始化节点失败: %v", err)
	}
	inv := m.CreatePairCode(10 * time.Minute)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(inv)
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dataDir := fs.String("data", envOr("DATA_DIR", "./data"), "数据根目录")
	_ = fs.Parse(args)

	layout := host.NewLayout(*dataDir)
	cfg, _ := host.LoadHostConfig(layout.HostConfigPath())
	m, err := mesh.OpenMesh(layout.HostDir, cfg.NodeName, cfg.NetworkTag, "")
	if err != nil {
		fatalf("读取节点状态失败: %v", err)
	}
	fmt.Printf("Node ID:    %s\nNode Name:  %s %s\nHost Dir:   %s\nPeers (%d):\n",
		m.Identity.NodeID, cfg.NodeName, cfg.NetworkTag, filepath.Clean(layout.HostDir), len(m.Peers()))
	for _, p := range m.Peers() {
		fmt.Printf("  - %s %s (bots: %v)\n", p.NodeID, p.NetworkTag, p.Bots)
	}
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
