package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"frogbot/internal/agy"
	"frogbot/internal/auth"
	"frogbot/internal/bot"
	"frogbot/internal/remote"
)

const (
	systemdUnitFile = "/etc/systemd/system/frogbot.service"
	systemdEnvFile  = "/etc/frogbot/frogbot.env"
	rootdUnitFile   = "/etc/systemd/system/frogbot-rootd.service"
	rootdStateFile  = "/etc/frogbot/rootd.json"
)

type cliEnv struct {
	dataDir       string
	workspace     string
	logFile       string
	configFile    string
	isSystemd     bool
	isUserSystemd bool
	isLaunchd     bool
	envFile       string
	systemdUser   string
	systemdAddr   string
	systemdMode   string
}

type cliConfig struct {
	Mode       string `json:"mode,omitempty"`        // "local" | "server"
	Addr       string `json:"addr,omitempty"`        // 例如 "127.0.0.1:8080" 或 "0.0.0.0:8080"
	Port       int    `json:"port,omitempty"`        // 例如 8080
	NoBrowser  bool   `json:"no_browser,omitempty"`
	AutoRemote *bool  `json:"auto_remote,omitempty"` // 默认 true
}

func resolveCLIEnv(dataOverride string) cliEnv {
	e := cliEnv{}
	explicitData := strings.TrimSpace(dataOverride) != "" || strings.TrimSpace(os.Getenv("DATA_DIR")) != ""

	if runtime.GOOS == "linux" && !explicitData {
		if _, err := os.Stat(systemdUnitFile); err == nil {
			e.isSystemd = true
			e.envFile = systemdEnvFile
			kv := parseEnvFile(systemdEnvFile)
			e.systemdUser = kv["FROGBOT_RUN_USER"]
			e.systemdAddr = kv["ADDR"]
			e.systemdMode = kv["FROGBOT_MODE"]
			if d := strings.TrimSpace(kv["DATA_DIR"]); d != "" {
				e.dataDir = d
			} else {
				e.dataDir = "/var/lib/frogbot/data"
			}
			if w := strings.TrimSpace(kv["WORKSPACE"]); w != "" {
				e.workspace = w
			} else {
				e.workspace = filepath.Join(filepath.Dir(e.dataDir), "workspace")
			}
		} else if _, err := os.Stat(linuxUserUnitPath()); err == nil {
			e.isUserSystemd = true
		}
	}
	if runtime.GOOS == "darwin" && !explicitData {
		if _, err := os.Stat(macPlistPath()); err == nil {
			e.isLaunchd = true
		}
	}

	if e.dataDir == "" {
		e.dataDir, e.workspace = dirs(dataOverride)
	}
	e.dataDir = absPath(e.dataDir)
	e.workspace = absPath(e.workspace)
	e.configFile = filepath.Join(e.dataDir, "cli.json")
	if lf := strings.TrimSpace(os.Getenv("FROGBOT_LOG_FILE")); lf != "" {
		e.logFile = lf
	} else {
		e.logFile = filepath.Join(filepath.Dir(e.dataDir), "frog.log")
	}
	return e
}

func parseEnvFile(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k != "" {
			out[k] = v
		}
	}
	return out
}

func updateEnvFileKey(path, key, value string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	found := false
	for i, l := range lines {
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, key+"=") {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	content := strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0o640)
}

func loadCLIConfig(env cliEnv) cliConfig {
	var cfg cliConfig
	if raw, err := os.ReadFile(env.configFile); err == nil {
		_ = json.Unmarshal(raw, &cfg)
	}
	if cfg.Mode == "" {
		if env.isSystemd && strings.EqualFold(env.systemdMode, "server") {
			cfg.Mode = "server"
		} else {
			cfg.Mode = "local"
		}
	}
	if cfg.Addr == "" && env.isSystemd && env.systemdAddr != "" {
		cfg.Addr = env.systemdAddr
	}
	return cfg
}

func saveCLIConfig(env cliEnv, cfg cliConfig) error {
	if err := os.MkdirAll(env.dataDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(env.configFile, raw, 0o600)
}

func resolveSelfExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if realExe, err := filepath.EvalSymlinks(exe); err == nil {
		exe = realExe
	}
	return strings.TrimSuffix(exe, " (deleted)"), nil
}

func buildServeArgs(env cliEnv, cfg cliConfig) []string {
	args := []string{"run", "-no-browser", "-data", env.dataDir}
	if strings.EqualFold(cfg.Mode, "server") {
		args = append(args, "-server")
	}
	if strings.TrimSpace(cfg.Addr) != "" {
		args = append(args, "-addr", strings.TrimSpace(cfg.Addr))
	} else if cfg.Port > 0 {
		args = append(args, "-port", strconv.Itoa(cfg.Port))
	}
	return args
}

func runCmdPassthrough(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ---------- CLI 帮助与主分发 ----------

func printCLIHelp() {
	fmt.Println()
	fmt.Print(renderFrogBanner([]string{
		fmt.Sprintf("frog %s  (%s/%s)", version, runtime.GOOS, runtime.GOARCH),
		"全平台极简服务端 CLI（兼容 frogbot 命令）",
		"",
		"直接运行 frog       打开交互式状态面板",
		"直接运行 frog pair  终端打印二维码给手机扫码",
	}, useANSIColor()))
	fmt.Print(`
用法：
  frog                     打开交互式状态面板与快捷菜单
  frog <命令> [参数]       直接执行对应管理操作

服务管理：
  frog start               后台启动服务（可选 --server / --port 8080 / --fg 前台）
  frog stop                优雅停止服务
  frog restart             重启服务
  frog status              查看运行状态、远程穿透、已装 CLI 与自启状态（别名 st）
  frog logs [-f] [-n 80]   查看日志（-f 实时追踪，Ctrl+C 退出）
  frog open                在浏览器打开网页管理界面

手机配对与设备：
  frog pair                在终端直接打印二维码与连接码给手机扫码（别名 qr / connect）
  frog devices             查看已配对设备；frog devices revoke <ID> 吊销设备

更新、配置与账号：
  frog update [beta|stable|版本]   一键检查并升级（带 SHA256 校验，自动重启）
  frog config              查看或修改配置（mode / port / relay / remote / name / channel）
  frog passwd              重置服务器登录密码（兼容 reset-password）
  frog service <install|uninstall|status>   配置/移除开机自启（systemd / launchd / 计划任务）
  frog uninstall [--purge] 停止并移除开机自启（--purge 可清理数据提示）

运维与中继：
  frog relay [--listen :18443] [--token ...]  启动公网 E2EE WSS 盲中继（--legacy 启动旧版中继）
  frog doctor [--fix]      诊断宿主机环境与 Coding CLI
  frog backup / restore / gc   加密备份、恢复与日志清理
  frog version             显示版本号

`)
}

func dispatchCLI(args []string) bool {
	if len(args) == 0 {
		// 兼容 Docker / systemd 无参数且设置了 FROGBOT_MODE=server 的非交互环境
		if strings.EqualFold(env("", "FROGBOT_MODE"), "server") && !isTerminal(os.Stdin) {
			return false
		}
		if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
			cliMenuMain()
			return true
		}
		printCLIHelp()
		return true
	}

	cmd := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]

	switch cmd {
	case "help", "-h", "--help":
		printCLIHelp()
		return true
	case "version", "-v", "-version", "--version":
		fmt.Printf("frog %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return true
	case "run", "serve":
		serveMain(rest)
		return true
	case "start":
		cliStart(rest)
		return true
	case "stop":
		cliStop(rest)
		return true
	case "restart":
		cliRestart(rest)
		return true
	case "status", "st":
		cliStatus(rest)
		return true
	case "logs", "log":
		cliLogs(rest)
		return true
	case "open":
		cliOpen(rest)
		return true
	case "pair", "qr", "connect":
		cliPair(rest)
		return true
	case "devices", "device":
		cliDevices(rest)
		return true
	case "update", "upgrade":
		cliUpdate(rest)
		return true
	case "passwd", "password", "reset-password":
		cliPasswd(rest)
		return true
	case "config", "cfg":
		cliConfigCmd(rest)
		return true
	case "service", "svc":
		cliServiceCmd(rest)
		return true
	case "uninstall":
		cliUninstall(rest)
		return true
	case "relay":
		cliRelay(rest)
		return true
	case "relay-ws":
		v2RelayWSMain(rest)
		return true
	case "doctor":
		v2DoctorMain(rest)
		return true
	case "backup":
		v2BackupMain(rest)
		return true
	case "restore":
		v2RestoreMain(rest)
		return true
	case "gc":
		v2GCMain(rest)
		return true
	case "exec":
		v2ExecMain(rest)
		return true
	case "rootd":
		rootdMain(rest)
		return true
	case "sandbox-exec":
		sandboxMain(rest)
		return true
	case "sandbox-check":
		sandboxCheckMain(rest)
		return true
	default:
		if strings.HasPrefix(args[0], "-") {
			// 兼容旧版直接传 flag（如 frogbot -server / -addr / -port / -no-browser）
			return false
		}
		fmt.Fprintf(os.Stderr, "未知命令：%s\n\n运行 frog help 查看可用指令。\n", args[0])
		os.Exit(2)
		return true
	}
}

// ---------- start / stop / restart / status / logs / open ----------

func cliStart(args []string) {
	fl := flag.NewFlagSet("start", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	serverFlag := fl.Bool("server", false, "以服务器模式启动（监听 0.0.0.0 且需要密码登录）")
	localFlag := fl.Bool("local", false, "以本机模式启动（监听 127.0.0.1，免登录）")
	addrFlag := fl.String("addr", "", "监听地址，如 127.0.0.1:8080")
	portFlag := fl.Int("port", 0, "监听端口")
	fgFlag := fl.Bool("fg", false, "在前台运行（不转入后台）")
	_ = fl.Parse(args)

	env := resolveCLIEnv(*dataFlag)
	cfg := loadCLIConfig(env)
	changed := false
	if *serverFlag {
		cfg.Mode = "server"
		changed = true
	} else if *localFlag {
		cfg.Mode = "local"
		changed = true
	}
	if strings.TrimSpace(*addrFlag) != "" {
		cfg.Addr = strings.TrimSpace(*addrFlag)
		changed = true
	}
	if *portFlag > 0 {
		cfg.Port = *portFlag
		changed = true
	}
	if changed && !env.isSystemd {
		_ = saveCLIConfig(env, cfg)
	}

	if *fgFlag {
		serveMain(buildServeArgs(env, cfg)[1:])
		return
	}

	if err := startDaemon(env, cfg); err != nil {
		fatal("%v", err)
	}
}

func startDaemon(env cliEnv, cfg cliConfig) error {
	if _, live, err := connectLiveControl(env.dataDir); err == nil && live != nil {
		fmt.Printf("✓ frog 已在运行（PID %d，%s）\n", live.PID, live.URL)
		if live.Remote != nil && live.Remote.StatusText != "" {
			fmt.Printf("  远程连接：%s\n", live.Remote.StatusText)
		}
		return nil
	}

	if env.isSystemd {
		fmt.Println("正在启动 systemd 服务 frogbot…")
		if _, err := os.Stat(rootdUnitFile); err == nil {
			_ = exec.Command("systemctl", "start", "frogbot-rootd").Run()
		}
		if err := runCmdPassthrough("systemctl", "start", "frogbot"); err != nil {
			return fmt.Errorf("启动失败（若无权限请用 sudo frog start）：%w", err)
		}
		return waitAndPrintStarted(env)
	}
	if env.isUserSystemd {
		fmt.Println("正在启动用户级 systemd 服务 frogbot…")
		if err := runCmdPassthrough("systemctl", "--user", "start", "frogbot"); err != nil {
			return fmt.Errorf("启动用户服务失败：%w", err)
		}
		return waitAndPrintStarted(env)
	}
	if env.isLaunchd {
		uid := fmt.Sprintf("gui/%d", os.Getuid())
		label := uid + "/" + macLaunchAgentLabel
		if exec.Command("launchctl", "kickstart", "-k", label).Run() == nil {
			return waitAndPrintStarted(env)
		}
		if exec.Command("launchctl", "bootstrap", uid, macPlistPath()).Run() == nil {
			return waitAndPrintStarted(env)
		}
	}

	exe, err := resolveSelfExecutable()
	if err != nil {
		return fmt.Errorf("无法定位当前程序路径：%w", err)
	}
	if err := os.MkdirAll(filepath.Dir(env.logFile), 0o755); err != nil {
		return fmt.Errorf("创建日志目录失败：%w", err)
	}
	// 日志超过 20 MB 自动轮转一份 .old
	if fi, err := os.Stat(env.logFile); err == nil && fi.Size() > 20<<20 {
		_ = os.Remove(env.logFile + ".old")
		_ = os.Rename(env.logFile, env.logFile+".old")
	}
	logF, err := os.OpenFile(env.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开日志文件失败：%w", err)
	}
	defer logF.Close()

	serveArgs := buildServeArgs(env, cfg)
	cmd := exec.Command(exe, serveArgs...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Env = append(os.Environ(), "FROGBOT_NO_BROWSER=1", "FROGBOT_LOG_FILE="+env.logFile)
	if cfg.AutoRemote != nil && !*cfg.AutoRemote {
		cmd.Env = append(cmd.Env, "FROGBOT_AUTO_REMOTE=0")
	}
	configureDetachedCmd(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("后台启动失败：%w", err)
	}
	_ = cmd.Process.Release()

	return waitAndPrintStarted(env)
}

func waitAndPrintStarted(env cliEnv) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, live, err := connectLiveControl(env.dataDir); err == nil && live != nil {
			// 给内置 E2EE 中继一点时间建立首连（最多等 1.5s），方便状态直接展示就绪
			if live.Remote != nil && live.Remote.Mode == remote.ModeE2EE && !live.Remote.RelayConnected && live.Remote.EffectiveRelayURL != "" {
				for range 6 {
					time.Sleep(250 * time.Millisecond)
					if _, l2, err2 := connectLiveControl(env.dataDir); err2 == nil && l2 != nil {
						live = l2
						if l2.Remote != nil && l2.Remote.RelayConnected {
							break
						}
					}
				}
			}
			fmt.Printf("✓ frog %s 已在后台启动（PID %d · %s）\n", live.Version, live.PID, modeLabel(live.Mode))
			fmt.Printf("  管理页面：%s\n", live.URL)
			if live.Remote != nil && live.Remote.StatusText != "" {
				fmt.Printf("  远程连接：%s\n", live.Remote.StatusText)
			}
			fmt.Printf("  日志文件：%s\n", env.logFile)
			fmt.Println("  提示：运行 frog pair 直接在终端扫码连接手机；frog logs -f 查看日志。")
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	if tail := readLogTailLines(env.logFile, 12); tail != "" {
		return fmt.Errorf("服务 15 秒内未就绪，最近日志：\n%s", tail)
	}
	return errors.New("服务 15 秒内未就绪，请运行 frog logs 查看原因")
}

func cliStop(args []string) {
	fl := flag.NewFlagSet("stop", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	_ = fl.Parse(args)
	env := resolveCLIEnv(*dataFlag)
	if err := stopDaemon(env); err != nil {
		fatal("%v", err)
	}
}

func stopDaemon(env cliEnv) error {
	if env.isSystemd {
		if err := runCmdPassthrough("systemctl", "stop", "frogbot"); err != nil {
			return fmt.Errorf("停止 systemd 服务失败（可能需要 sudo frog stop）：%w", err)
		}
		fmt.Println("✓ frog 服务已停止")
		return nil
	}
	if env.isUserSystemd {
		if err := runCmdPassthrough("systemctl", "--user", "stop", "frogbot"); err != nil {
			return fmt.Errorf("停止用户服务失败：%w", err)
		}
		fmt.Println("✓ frog 服务已停止")
		return nil
	}
	if env.isLaunchd {
		uid := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", uid+"/"+macLaunchAgentLabel).Run()
		_ = exec.Command("launchctl", "unload", macPlistPath()).Run()
	}

	cc, live, err := connectLiveControl(env.dataDir)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New("无权限访问运行状态文件，请尝试 sudo frog stop")
		}
		// 清理可能残留的 runtime.json
		if st, rerr := readRuntimeState(env.dataDir); rerr == nil {
			if st.PID > 0 && isProcessAlive(st.PID) {
				_ = terminateProcess(st.PID, false)
				waitPIDExit(st.PID, 4*time.Second)
				if isProcessAlive(st.PID) {
					_ = terminateProcess(st.PID, true)
				}
			}
			_ = os.Remove(runtimeStatePath(env.dataDir))
		}
		fmt.Println("frog 当前未在运行。")
		return nil
	}

	pid := live.PID
	_ = cc.Stop()
	if !waitPIDExit(pid, 5*time.Second) && isProcessAlive(pid) {
		_ = terminateProcess(pid, false)
		if !waitPIDExit(pid, 3*time.Second) && isProcessAlive(pid) {
			_ = terminateProcess(pid, true)
		}
	}
	removeRuntimeStateIfOwned(env.dataDir, pid)
	fmt.Printf("✓ frog 已停止（原 PID %d）\n", pid)
	return nil
}

func waitPIDExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isProcessAlive(pid) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return !isProcessAlive(pid)
}

func cliRestart(args []string) {
	fl := flag.NewFlagSet("restart", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	_ = fl.Parse(args)
	env := resolveCLIEnv(*dataFlag)
	if err := restartDaemon(env); err != nil {
		fatal("%v", err)
	}
}

func restartDaemon(env cliEnv) error {
	if env.isSystemd {
		fmt.Println("正在重启 systemd 服务 frogbot…")
		if err := runCmdPassthrough("systemctl", "restart", "frogbot"); err != nil {
			return fmt.Errorf("重启失败（可能需要 sudo frog restart）：%w", err)
		}
		return waitAndPrintStarted(env)
	}
	if env.isUserSystemd {
		fmt.Println("正在重启用户级 systemd 服务 frogbot…")
		if err := runCmdPassthrough("systemctl", "--user", "restart", "frogbot"); err != nil {
			return fmt.Errorf("重启失败：%w", err)
		}
		return waitAndPrintStarted(env)
	}
	_ = stopDaemon(env)
	return startDaemon(env, loadCLIConfig(env))
}

func cliStatus(args []string) {
	fl := flag.NewFlagSet("status", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	asJSON := fl.Bool("json", false, "输出 JSON")
	_ = fl.Parse(args)
	env := resolveCLIEnv(*dataFlag)
	printStatus(env, *asJSON)
}

func printStatus(env cliEnv, asJSON bool) {
	_, live, err := connectLiveControl(env.dataDir)
	_, svcDetail := serviceStatusSummary(env)

	if asJSON {
		out := map[string]any{
			"running":     live != nil,
			"cli_version": version,
			"data_dir":    env.dataDir,
			"workspace":   env.workspace,
			"log_file":    env.logFile,
			"service":     svcDetail,
		}
		if live != nil {
			out["live"] = live
		} else if err != nil {
			out["error"] = err.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}

	fmt.Println()
	if live != nil {
		up := formatDurationBrief(time.Since(live.StartedAt))
		info := []string{
			fmt.Sprintf("frog %s   ● 运行中（PID %d · 运行 %s · %s）", live.Version, live.PID, up, modeLabel(live.Mode)),
			fmt.Sprintf("管理页面：%s", live.URL),
		}
		if live.Remote != nil && live.Remote.StatusText != "" {
			info = append(info, fmt.Sprintf("远程穿透：%s（已配对 %d 台）", live.Remote.StatusText, len(live.Remote.Devices)))
		}
		if names := installedAgentNames(live.Agents); len(names) > 0 {
			info = append(info, fmt.Sprintf("Coding CLI：%s", strings.Join(names, ", ")))
		}
		info = append(info, fmt.Sprintf("开机自启：%s", svcDetail))
		fmt.Print(renderFrogBanner(info, useANSIColor()))
		fmt.Println()
		fmt.Printf("  数据目录： %s\n", live.DataDir)
		fmt.Printf("  工作区：   %s\n", live.Workspace)
		if live.LogFile != "" {
			fmt.Printf("  日志文件： %s\n", live.LogFile)
		}
		if live.Remote != nil {
			if live.Remote.EffectiveRelayURL != "" {
				fmt.Printf("  中继地址： %s\n", live.Remote.EffectiveRelayURL)
			}
			if live.Remote.E2EELANAddr != "" {
				fmt.Printf("  局域网直连：%s\n", live.Remote.E2EELANAddr)
			}
		}
		fmt.Println()
	} else {
		cfg := loadCLIConfig(env)
		title := fmt.Sprintf("frog %s   ○ 未运行（%s）", version, modeLabel(cfg.Mode))
		if errors.Is(err, os.ErrPermission) {
			title = fmt.Sprintf("frog %s   ● 运行中（请用 sudo frog status 查看实时详情）", version)
		}
		info := []string{
			title,
			fmt.Sprintf("数据目录：%s", env.dataDir),
			fmt.Sprintf("日志文件：%s", env.logFile),
		}
		if names := installedAgentNames(agy.DiscoverAgents()); len(names) > 0 {
			info = append(info, fmt.Sprintf("Coding CLI：%s", strings.Join(names, ", ")))
		}
		info = append(info, fmt.Sprintf("开机自启：%s", svcDetail))
		fmt.Print(renderFrogBanner(info, useANSIColor()))
		fmt.Println()
	}
}

func installedAgentNames(agents []agy.AgentCLIView) []string {
	var out []string
	for _, a := range agents {
		if a.Installed {
			out = append(out, a.Name)
		}
	}
	return out
}

func cliLogs(args []string) {
	fl := flag.NewFlagSet("logs", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	follow := fl.Bool("f", false, "实时追踪日志")
	lines := fl.Int("n", 80, "显示最后 N 行")
	_ = fl.Parse(args)
	env := resolveCLIEnv(*dataFlag)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := showLogs(ctx, env, *lines, *follow); err != nil {
		fatal("%v", err)
	}
}

func showLogs(ctx context.Context, env cliEnv, lines int, follow bool) error {
	if lines <= 0 {
		lines = 80
	}
	if env.isSystemd {
		jargs := []string{"-u", "frogbot", "-n", strconv.Itoa(lines), "--no-pager"}
		if follow {
			jargs = append(jargs, "-f")
		}
		cmd := exec.CommandContext(ctx, "journalctl", jargs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if env.isUserSystemd {
		jargs := []string{"--user", "-u", "frogbot", "-n", strconv.Itoa(lines), "--no-pager"}
		if follow {
			jargs = append(jargs, "-f")
		}
		cmd := exec.CommandContext(ctx, "journalctl", jargs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	return tailLogFile(ctx, env.logFile, lines, follow, os.Stdout)
}

func readLogTailLines(path string, n int) string {
	var buf bytes.Buffer
	_ = tailLogFile(context.Background(), path, n, false, &buf)
	return strings.TrimSpace(buf.String())
}

func tailLogFile(ctx context.Context, path string, n int, follow bool, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(w, "暂无日志文件（%s）。先运行 frog start 启动服务。\n", path)
			return nil
		}
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()
	const maxRead = 256 * 1024
	start := size - maxRead
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if start > 0 {
		if idx := bytes.IndexByte(raw, '\n'); idx >= 0 {
			raw = raw[idx+1:]
		}
	}
	allLines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(allLines) > n {
		allLines = allLines[len(allLines)-n:]
	}
	if len(allLines) > 0 && !(len(allLines) == 1 && allLines[0] == "") {
		fmt.Fprintln(w, strings.Join(allLines, "\n"))
	}
	if !follow {
		return nil
	}

	offset := size
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	buf := make([]byte, 16*1024)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			st, serr := os.Stat(path)
			if serr != nil {
				continue
			}
			if st.Size() < offset {
				// 日志被轮转或截断，重新打开
				_ = f.Close()
				nf, oerr := os.Open(path)
				if oerr != nil {
					continue
				}
				f = nf
				offset = 0
			}
			if st.Size() > offset {
				if _, serr := f.Seek(offset, io.SeekStart); serr != nil {
					continue
				}
				for {
					rn, rerr := f.Read(buf)
					if rn > 0 {
						_, _ = w.Write(buf[:rn])
						offset += int64(rn)
					}
					if rerr != nil {
						break
					}
				}
			}
		}
	}
}

func cliOpen(args []string) {
	fl := flag.NewFlagSet("open", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	_ = fl.Parse(args)
	env := resolveCLIEnv(*dataFlag)
	_, live, err := connectLiveControl(env.dataDir)
	if err != nil || live == nil {
		fatal("frog 当前未运行，请先执行 frog start")
	}
	fmt.Printf("正在打开 %s …\n", live.URL)
	if err := openBrowser(live.URL); err != nil {
		fmt.Printf("无法自动打开浏览器（%v），请手动访问：%s\n", err, live.URL)
	}
}

// ---------- pair / devices ----------

func cliPair(args []string) {
	fl := flag.NewFlagSet("pair", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	noQR := fl.Bool("no-qr", false, "只打印连接码，不打印二维码")
	plain := fl.Bool("plain", false, "使用纯黑白字符二维码（不输出 ANSI 颜色）")
	autoStart := fl.Bool("start", false, "若服务未运行则自动先启动")
	_ = fl.Parse(args)
	env := resolveCLIEnv(*dataFlag)
	if err := runPair(env, *noQR, *plain, *autoStart); err != nil {
		fatal("%v", err)
	}
}

func runPair(env cliEnv, noQR, plain, autoStart bool) error {
	cc, live, err := connectLiveControl(env.dataDir)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New("无权限读取服务配对通道，请运行 sudo frog pair")
		}
		if autoStart || (isTerminal(os.Stdin) && promptYesNo("frog 服务当前未运行，是否现在启动？", true)) {
			if serr := startDaemon(env, loadCLIConfig(env)); serr != nil {
				return serr
			}
			cc, live, err = connectLiveControl(env.dataDir)
			if err != nil {
				return err
			}
		} else {
			return errors.New("frog 当前未运行，请先执行 frog start（或加 --start 自动启动）")
		}
	}

	pair, err := cc.Pair(true)
	if err != nil {
		return fmt.Errorf("生成配对码失败：%w", err)
	}

	fmt.Println()
	if live != nil && live.Remote != nil && live.Remote.StatusText != "" {
		fmt.Printf("  远程通道：%s\n", live.Remote.StatusText)
	}
	if !noQR {
		useColor := !plain && os.Getenv("NO_COLOR") == ""
		if qr, qerr := remote.RenderQRTerminal(pair.PairCode, useColor); qerr == nil {
			fmt.Println()
			fmt.Print(qr)
		}
	}
	fmt.Printf("\n  连接码（有效期至 %s，10 分钟内有效）：\n", pair.ExpiresAt.Local().Format("15:04:05"))
	fmt.Printf("  %s\n\n", pair.PairCode)
	fmt.Println("  在 iOS / Mac / Android / Windows 客户端点击「扫码 / 粘贴连接码」即可端到端加密接入。")
	fmt.Println()
	return nil
}

func cliDevices(args []string) {
	fl := flag.NewFlagSet("devices", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	_ = fl.Parse(args)
	rest := fl.Args()
	env := resolveCLIEnv(*dataFlag)

	cc, _, err := connectLiveControl(env.dataDir)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			fatal("无权限访问设备列表，请运行 sudo frog devices")
		}
		fatal("frog 当前未运行，请先执行 frog start")
	}

	if len(rest) >= 2 && (rest[0] == "revoke" || rest[0] == "rm" || rest[0] == "delete") {
		id := strings.TrimSpace(rest[1])
		sv, err := cc.RevokeDevice(id)
		if err != nil {
			fatal("吊销失败：%v", err)
		}
		fmt.Printf("✓ 已吊销设备 %s（当前剩余 %d 台已配对设备）\n", id, len(sv.Devices))
		return
	}

	devs, err := cc.Devices()
	if err != nil {
		fatal("获取设备列表失败：%v", err)
	}
	if len(devs) == 0 {
		fmt.Println("暂无已配对设备。运行 frog pair 生成配对二维码。")
		return
	}
	fmt.Printf("\n  已配对设备（共 %d 台）：\n\n", len(devs))
	for i, d := range devs {
		last := "从未"
		if !d.LastSeenAt.IsZero() {
			last = d.LastSeenAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Printf("  [%d] %-18s  ID: %-18s  最近在线：%s\n", i+1, d.Name, d.ID, last)
	}
	fmt.Println("\n  吊销指定设备：frog devices revoke <ID>")
}

// ---------- update ----------

func cliUpdate(args []string) {
	fl := flag.NewFlagSet("update", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	checkOnly := fl.Bool("check", false, "仅检查更新，不安装")
	force := fl.Bool("force", false, "即使版本号相同也强制重新下载安装")
	_ = fl.Parse(args)
	rest := fl.Args()

	env := resolveCLIEnv(*dataFlag)
	var channel, targetTag string
	if len(rest) > 0 {
		arg := strings.TrimSpace(rest[0])
		switch strings.ToLower(arg) {
		case "beta", "stable":
			channel = strings.ToLower(arg)
		default:
			if !bot.ValidReleaseTag(arg) {
				if bot.ValidReleaseTag("v" + arg) {
					arg = "v" + arg
				} else {
					fatal("无效的版本或通道：%q（示例：frog update / frog update beta / frog update stable / frog update v0.1.11-beta.10）", arg)
				}
			}
			targetTag = arg
		}
	}
	if err := runUpdate(env, channel, targetTag, *checkOnly, *force); err != nil {
		fatal("%v", err)
	}
}

func runUpdate(env cliEnv, channel, targetTag string, checkOnly, force bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	curVer := version
	if channel == "" {
		if targetTag != "" && strings.Contains(targetTag, "-") {
			channel = "beta"
		} else {
			channel = bot.LoadUpdateChannel(env.dataDir, curVer)
		}
	} else {
		bot.SaveUpdateChannel(env.dataDir, channel, curVer)
	}

	fmt.Printf("正在检查更新（当前：%s，通道：%s）…\n", curVer, channel)
	st, err := bot.CheckUpdateChannel(ctx, curVer, channel)
	if err != nil {
		return fmt.Errorf("检查更新失败：%w", err)
	}

	if targetTag == "" {
		targetTag = st.Latest
	}
	if targetTag == "" {
		fmt.Println("当前通道暂无可用发布版本。")
		return nil
	}

	fmt.Printf("  当前版本：%s\n  目标版本：%s（通道：%s）\n", curVer, targetTag, st.Channel)
	if checkOnly {
		if st.State == "available" {
			fmt.Printf("  有可用更新！执行 frog update %s 即可一键升级。\n", st.Channel)
		} else {
			fmt.Println("  ✓ 已是最新版本。")
		}
		return nil
	}

	if !force && targetTag == curVer {
		fmt.Println("✓ 已是最新版本，无需升级（加 --force 可强制重装）。")
		return nil
	}

	_, live, _ := connectLiveControl(env.dataDir)
	wasRunning := live != nil
	if env.isSystemd && !wasRunning {
		if out, err := exec.Command("systemctl", "is-active", "frogbot").Output(); err == nil && strings.TrimSpace(string(out)) == "active" {
			wasRunning = true
		}
	}

	if wasRunning {
		fmt.Println("正在暂停运行中的服务以便升级…")
		if err := stopDaemon(env); err != nil {
			return fmt.Errorf("停止服务失败（Linux systemd 请用 sudo frog update）：%w", err)
		}
	}

	fmt.Printf("正在下载并校验 frog %s（%s/%s）…\n", targetTag, runtime.GOOS, runtime.GOARCH)
	replacedPath, err := bot.SelfReplaceBinary(ctx, targetTag)
	if err != nil {
		if wasRunning {
			_ = startDaemon(env, loadCLIConfig(env))
		}
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("无写入权限，请使用 sudo frog update（%w）", err)
		}
		return fmt.Errorf("升级失败：%w", err)
	}
	fmt.Printf("✓ 已更新程序：%s -> %s\n", replacedPath, targetTag)

	if wasRunning {
		fmt.Println("正在重新启动服务…")
		if err := startDaemon(env, loadCLIConfig(env)); err != nil {
			return fmt.Errorf("升级成功但重启失败：%w", err)
		}
	} else {
		fmt.Println("运行 frog start 即可启动新版本。")
	}
	return nil
}

// ---------- passwd ----------

func cliPasswd(args []string) {
	// 若带了传统脚本参数（-password-stdin / -quiet / -random）或非交互模式，直接复用 resetPassword
	for _, a := range args {
		if a == "-password-stdin" || a == "--password-stdin" || a == "-quiet" || a == "--quiet" || a == "-random" || a == "--random" {
			cleaned := make([]string, 0, len(args))
			for _, x := range args {
				if x != "-random" && x != "--random" {
					cleaned = append(cleaned, x)
				}
			}
			resetPassword(cleaned)
			return
		}
	}

	fl := flag.NewFlagSet("passwd", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	userFlag := fl.String("user", "", "用户名")
	_ = fl.Parse(args)

	env := resolveCLIEnv(*dataFlag)
	name := strings.TrimSpace(*userFlag)
	if name == "" {
		if a, err := auth.Open(env.dataDir); err == nil {
			name = a.Username()
		} else {
			name = envStr("admin", "FROGBOT_USER")
		}
	}

	var pw string
	if isTerminal(os.Stdin) {
		fmt.Printf("重置账号「%s」密码（留空直接回车将自动生成强随机密码）：", name)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		pw = strings.TrimSpace(line)
	}
	generated := false
	if pw == "" {
		pw = auth.RandomPassword()
		generated = true
	}
	if err := auth.SetPassword(env.dataDir, name, pw); err != nil {
		fatal("设置密码失败：%v", err)
	}
	if env.isSystemd {
		fixSystemdDataOwnership(env)
		syncRootdAfterReset(env.dataDir)
		if _, err := os.Stat(rootdStateFile); err == nil && os.Geteuid() == 0 {
			_ = exec.Command("systemctl", "try-restart", "frogbot-rootd").Run()
		}
	} else {
		syncRootdAfterReset(env.dataDir)
	}
	fmt.Printf("✓ 已更新登录密码（旧登录会话已全部失效）\n  用户名：%s\n", name)
	if generated {
		fmt.Printf("  新密码：%s\n", pw)
	}
	fmt.Println("  正在运行的 frog 会自动生效，无需重启。")
}

func fixSystemdDataOwnership(env cliEnv) {
	if runtime.GOOS != "linux" || !env.isSystemd || os.Geteuid() != 0 || env.systemdUser == "" || env.systemdUser == "root" {
		return
	}
	_ = exec.Command("chown", "-R", env.systemdUser+":"+env.systemdUser, filepath.Join(env.dataDir, "auth.json")).Run()
}

// ---------- config ----------

func cliConfigCmd(args []string) {
	fl := flag.NewFlagSet("config", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	_ = fl.Parse(args)
	rest := fl.Args()
	env := resolveCLIEnv(*dataFlag)

	if len(rest) == 0 || rest[0] == "show" || rest[0] == "list" {
		printConfig(env)
		return
	}

	// 支持 frog config set <k> <v> 和简写 frog config <k> <v>
	if rest[0] == "set" {
		rest = rest[1:]
	}
	if len(rest) < 2 {
		fmt.Println(`用法：
  frog config                       查看当前配置
  frog config mode local|server     切换本机模式 / 服务器模式
  frog config port 8080             设置监听端口
  frog config addr 0.0.0.0:8080     设置监听地址
  frog config remote e2ee|reality|p2p|off   切换远程穿透模式
  frog config relay default|off|wss://...   设置 E2EE 中继地址
  frog config name 我的家用电脑       设置显示给手机的设备名称
  frog config channel beta|stable   切换自动更新通道`)
		return
	}

	key := strings.ToLower(strings.TrimSpace(rest[0]))
	val := strings.TrimSpace(strings.Join(rest[1:], " "))
	if err := applyConfigKV(env, key, val); err != nil {
		fatal("%v", err)
	}
}

func printConfig(env cliEnv) {
	cfg := loadCLIConfig(env)
	ch := bot.LoadUpdateChannel(env.dataDir, version)
	fmt.Println()
	fmt.Printf("  运行模式： %s\n", modeLabel(cfg.Mode))
	addr := cfg.Addr
	if addr == "" && cfg.Port > 0 {
		addr = fmt.Sprintf(":%d", cfg.Port)
	}
	if addr == "" {
		if cfg.Mode == "server" {
			addr = "0.0.0.0:8080（默认）"
		} else {
			addr = "127.0.0.1:8080（自动顺延）"
		}
	}
	fmt.Printf("  监听地址： %s\n", addr)
	fmt.Printf("  数据目录： %s\n", env.dataDir)
	fmt.Printf("  工作区：   %s\n", env.workspace)
	fmt.Printf("  更新通道： %s\n", ch)

	if _, live, err := connectLiveControl(env.dataDir); err == nil && live != nil && live.Remote != nil {
		rm := live.Remote
		fmt.Printf("  远程模式： %s（%s）\n", rm.Mode, rm.StatusText)
		fmt.Printf("  设备名称： %s\n", rm.ServerName)
		if rm.EffectiveRelayURL != "" {
			tag := "自定义"
			if rm.UsingDefaultRelay {
				tag = "内置默认"
			}
			fmt.Printf("  E2EE 中继：%s（%s）\n", rm.EffectiveRelayURL, tag)
		}
	} else if raw, err := os.ReadFile(filepath.Join(env.dataDir, "remote.json")); err == nil {
		var rc remote.Config
		if json.Unmarshal(raw, &rc) == nil {
			fmt.Printf("  远程模式： %s\n", rc.Mode)
			if rc.ServerName != "" {
				fmt.Printf("  设备名称： %s\n", rc.ServerName)
			}
			relay := rc.E2EERelayURL
			if relay == "" {
				relay = remote.ResolveDefaultE2EERelayURL("") + "（内置默认）"
			}
			fmt.Printf("  E2EE 中继：%s\n", relay)
		}
	}
	fmt.Println()
}

func applyConfigKV(env cliEnv, key, val string) error {
	switch key {
	case "channel":
		ch := strings.ToLower(val)
		if ch != "beta" && ch != "stable" {
			return errors.New("channel 只能为 beta 或 stable")
		}
		bot.SaveUpdateChannel(env.dataDir, ch, version)
		fmt.Printf("✓ 已将更新通道切换为 %s\n", ch)
		return nil

	case "mode":
		m := strings.ToLower(val)
		if m != "local" && m != "server" {
			return errors.New("mode 只能为 local 或 server")
		}
		cfg := loadCLIConfig(env)
		cfg.Mode = m
		if env.isSystemd {
			if err := updateEnvFileKey(env.envFile, "FROGBOT_MODE", m); err != nil {
				return fmt.Errorf("写入 %s 失败（可能需要 sudo）：%w", env.envFile, err)
			}
		} else if err := saveCLIConfig(env, cfg); err != nil {
			return err
		}
		fmt.Printf("✓ 已将运行模式设为 %s（运行 frog restart 生效）\n", modeLabel(m))
		return nil

	case "port":
		p, err := strconv.Atoi(val)
		if err != nil || p < 1 || p > 65535 {
			return errors.New("port 必须是 1-65535 的数字")
		}
		cfg := loadCLIConfig(env)
		cfg.Port = p
		cfg.Addr = ""
		if env.isSystemd {
			host := "0.0.0.0"
			if strings.HasPrefix(env.systemdAddr, "127.0.0.1:") {
				host = "127.0.0.1"
			}
			if err := updateEnvFileKey(env.envFile, "ADDR", fmt.Sprintf("%s:%d", host, p)); err != nil {
				return fmt.Errorf("写入 %s 失败（可能需要 sudo）：%w", env.envFile, err)
			}
		} else if err := saveCLIConfig(env, cfg); err != nil {
			return err
		}
		fmt.Printf("✓ 已将端口设为 %d（运行 frog restart 生效）\n", p)
		return nil

	case "addr":
		cfg := loadCLIConfig(env)
		cfg.Addr = val
		cfg.Port = 0
		if env.isSystemd {
			if err := updateEnvFileKey(env.envFile, "ADDR", val); err != nil {
				return fmt.Errorf("写入 %s 失败（可能需要 sudo）：%w", env.envFile, err)
			}
		} else if err := saveCLIConfig(env, cfg); err != nil {
			return err
		}
		fmt.Printf("✓ 已将监听地址设为 %s（运行 frog restart 生效）\n", val)
		return nil

	case "remote", "relay", "name":
		return applyRemoteConfigKV(env, key, val)

	default:
		return fmt.Errorf("不支持的配置项：%s（支持 mode / port / addr / remote / relay / name / channel）", key)
	}
}

func applyRemoteConfigKV(env cliEnv, key, val string) error {
	cc, live, err := connectLiveControl(env.dataDir)
	if err != nil || live == nil || live.Remote == nil {
		// 离线直接编辑 remote.json
		p := filepath.Join(env.dataDir, "remote.json")
		var rc remote.Config
		if raw, rerr := os.ReadFile(p); rerr == nil {
			_ = json.Unmarshal(raw, &rc)
		}
		switch key {
		case "remote":
			m := strings.ToLower(val)
			if m == "on" {
				m = remote.ModeE2EE
			}
			rc.Mode = m
		case "relay":
			if strings.EqualFold(val, "default") {
				rc.E2EERelayURL = ""
			} else {
				rc.E2EERelayURL = val
			}
		case "name":
			rc.ServerName = val
		}
		if err := os.MkdirAll(env.dataDir, 0o700); err != nil {
			return err
		}
		raw, _ := json.MarshalIndent(rc, "", "  ")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			return err
		}
		fmt.Printf("✓ 已保存远程配置 %s=%s（下次 frog start 生效）\n", key, val)
		return nil
	}

	cur := live.Remote
	in := remote.SaveInput{
		Mode:           cur.Mode,
		RealityListen:  cur.RealityListen,
		P2PListen:      cur.P2PListen,
		RelayAddr:      cur.RelayAddr,
		RelayToken:     cur.RelayToken,
		E2EERelayURL:   cur.E2EERelayURL,
		E2EERelayToken: cur.E2EERelayToken,
		E2EELANListen:  cur.E2EELANListen,
		PublicHost:     cur.PublicHost,
		SNI:            cur.SNI,
		ServerName:     cur.ServerName,
	}
	switch key {
	case "remote":
		m := strings.ToLower(val)
		if m == "on" {
			m = remote.ModeE2EE
		}
		in.Mode = m
	case "relay":
		if strings.EqualFold(val, "default") {
			in.E2EERelayURL = ""
		} else {
			in.E2EERelayURL = val
		}
	case "name":
		in.ServerName = val
	}
	sv, err := cc.UpdateRemoteConfig(in)
	if err != nil {
		return fmt.Errorf("更新远程配置失败：%w", err)
	}
	fmt.Printf("✓ 已实时生效：%s（模式：%s）\n", sv.StatusText, sv.Mode)
	return nil
}

// ---------- service / uninstall / relay ----------

func cliServiceCmd(args []string) {
	fl := flag.NewFlagSet("service", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	_ = fl.Parse(args)
	rest := fl.Args()
	env := resolveCLIEnv(*dataFlag)

	sub := "status"
	if len(rest) > 0 {
		sub = strings.ToLower(strings.TrimSpace(rest[0]))
	}
	switch sub {
	case "install", "enable":
		if err := installAutostartService(env, loadCLIConfig(env)); err != nil {
			fatal("%v", err)
		}
		_, detail := serviceStatusSummary(env)
		fmt.Printf("✓ 已开启开机自启：%s\n", detail)
	case "uninstall", "disable", "remove":
		if err := uninstallAutostartService(env); err != nil {
			fatal("%v", err)
		}
		fmt.Println("✓ 已关闭并移除开机自启服务")
	case "status":
		_, detail := serviceStatusSummary(env)
		fmt.Println("开机自启状态：" + detail)
	default:
		fatal("用法：frog service [install | uninstall | status]")
	}
}

func cliUninstall(args []string) {
	fl := flag.NewFlagSet("uninstall", flag.ExitOnError)
	dataFlag := fl.String("data", "", "数据目录")
	purge := fl.Bool("purge", false, "同时删除数据目录（不可恢复）")
	_ = fl.Parse(args)
	env := resolveCLIEnv(*dataFlag)

	_ = stopDaemon(env)
	_ = uninstallAutostartService(env)
	if *purge {
		if isTerminal(os.Stdin) && !promptYesNo(fmt.Sprintf("确认永久删除数据目录 %s ？", env.dataDir), false) {
			fmt.Println("已取消删除数据。")
			return
		}
		_ = os.RemoveAll(env.dataDir)
		fmt.Printf("✓ 已删除数据目录：%s\n", env.dataDir)
	} else {
		fmt.Printf("✓ 已停止并移除开机自启。数据保留在：%s\n", env.dataDir)
	}
}

func cliRelay(args []string) {
	legacy := false
	filtered := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--legacy" || a == "-legacy" {
			legacy = true
			continue
		}
		filtered = append(filtered, a)
	}
	if legacy {
		relayMain(filtered)
		return
	}
	v2RelayWSMain(filtered)
}

// ---------- 无参数交互式状态面板与菜单 ----------

func cliMenuMain() {
	reader := bufio.NewReader(os.Stdin)
	env := resolveCLIEnv("")

	for {
		_, live, _ := connectLiveControl(env.dataDir)
		installedSvc, _ := serviceStatusSummary(env)

		fmt.Println()
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		if live != nil {
			info := []string{
				fmt.Sprintf("frog %s   ● 运行中（PID %d · %s）", live.Version, live.PID, modeLabel(live.Mode)),
				fmt.Sprintf("网页管理：%s", live.URL),
			}
			if live.Remote != nil && live.Remote.StatusText != "" {
				info = append(info, fmt.Sprintf("远程穿透：%s（已配对 %d 台）", live.Remote.StatusText, len(live.Remote.Devices)))
			}
			if names := installedAgentNames(live.Agents); len(names) > 0 {
				info = append(info, fmt.Sprintf("Coding CLI：%s", strings.Join(names, ", ")))
			}
			fmt.Print(renderFrogBanner(info, useANSIColor()))
		} else {
			cfg := loadCLIConfig(env)
			info := []string{
				fmt.Sprintf("frog %s   ○ 未运行（%s）", version, modeLabel(cfg.Mode)),
				fmt.Sprintf("数据目录：%s", env.dataDir),
				"输入 1 后台启动，或输入 2 直接启动并扫码配对",
			}
			fmt.Print(renderFrogBanner(info, useANSIColor()))
		}
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

		if live != nil {
			fmt.Println("  [1] 重启服务              [2] 手机扫码配对（二维码）")
			fmt.Println("  [3] 查看实时日志          [4] 检查并一键更新")
			fmt.Println("  [5] 管理已配对设备        [6] 常用配置")
			fmt.Println("  [7] 重置登录密码          [8] 开机自启：" + map[bool]string{true: "已开启（点此管理）", false: "未开启（点此开启）"}[installedSvc])
			fmt.Println("  [9] 环境自检 (doctor)     [o] 打开网页管理")
			fmt.Println("  [s] 停止服务              [0] 退出面板")
		} else {
			fmt.Println("  [1] 后台启动服务          [2] 启动并生成手机配对二维码")
			fmt.Println("  [3] 查看历史日志          [4] 检查并一键更新")
			fmt.Println("  [6] 常用配置              [7] 重置登录密码")
			fmt.Println("  [8] 开机自启：" + map[bool]string{true: "已开启（点此管理）", false: "未开启（点此开启）"}[installedSvc])
			fmt.Println("  [9] 环境自检 (doctor)     [0] 退出面板")
		}
		fmt.Print("\n请输入选项并回车 > ")

		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println()
			return
		}
		choice := strings.ToLower(strings.TrimSpace(line))
		fmt.Println()

		switch choice {
		case "0", "q", "quit", "exit":
			return
		case "1":
			if live != nil {
				if err := restartDaemon(env); err != nil {
					fmt.Printf("✗ %v\n", err)
				}
			} else {
				if err := startDaemon(env, loadCLIConfig(env)); err != nil {
					fmt.Printf("✗ %v\n", err)
				}
			}
		case "2":
			if err := runPair(env, false, false, true); err != nil {
				fmt.Printf("✗ %v\n", err)
			}
		case "3":
			fmt.Println("按 Ctrl+C 返回主菜单…")
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			_ = showLogs(ctx, env, 60, live != nil)
			stop()
		case "4":
			fmt.Print("更新通道（回车沿用当前，或输入 beta / stable / 版本号）：")
			chLine, _ := reader.ReadString('\n')
			chArg := strings.TrimSpace(chLine)
			ch, tag := "", ""
			if chArg == "beta" || chArg == "stable" {
				ch = chArg
			} else if chArg != "" {
				tag = chArg
			}
			if err := runUpdate(env, ch, tag, false, false); err != nil {
				fmt.Printf("✗ %v\n", err)
			}
		case "5":
			if live == nil {
				fmt.Println("请先启动服务。")
				continue
			}
			cliDevices([]string{"-data", env.dataDir})
			fmt.Print("输入要吊销的设备 ID（直接回车返回）：")
			rev, _ := reader.ReadString('\n')
			if id := strings.TrimSpace(rev); id != "" {
				if cc, _, err := connectLiveControl(env.dataDir); err == nil {
					if _, rerr := cc.RevokeDevice(id); rerr != nil {
						fmt.Printf("✗ 吊销失败：%v\n", rerr)
					} else {
						fmt.Printf("✓ 已吊销设备 %s\n", id)
					}
				}
			}
		case "6":
			printConfig(env)
			fmt.Print("输入修改指令（如 port 8080 / mode server / relay default / name 家用机，回车返回）：")
			kvLine, _ := reader.ReadString('\n')
			parts := strings.Fields(kvLine)
			if len(parts) >= 2 {
				if err := applyConfigKV(env, strings.ToLower(parts[0]), strings.Join(parts[1:], " ")); err != nil {
					fmt.Printf("✗ %v\n", err)
				}
			}
		case "7":
			cliPasswd([]string{"-data", env.dataDir})
		case "8":
			if installedSvc {
				if promptYesNo("当前已开启开机自启，是否关闭并移除？", false) {
					if err := uninstallAutostartService(env); err != nil {
						fmt.Printf("✗ %v\n", err)
					} else {
						fmt.Println("✓ 已关闭开机自启")
					}
				}
			} else {
				if err := installAutostartService(env, loadCLIConfig(env)); err != nil {
					fmt.Printf("✗ %v\n", err)
				} else {
					fmt.Println("✓ 已开启开机自启")
				}
			}
		case "9":
			v2DoctorMain([]string{"-data", env.dataDir})
		case "o":
			if live != nil {
				if err := openBrowser(live.URL); err != nil {
					fmt.Printf("请手动打开：%s\n", live.URL)
				}
			}
		case "s":
			if live != nil {
				if err := stopDaemon(env); err != nil {
					fmt.Printf("✗ %v\n", err)
				}
			}
		case "":
			continue
		default:
			fmt.Println("无效选项，请输入菜单中对应的数字或字母。")
		}
	}
}

func promptYesNo(prompt string, defYes bool) bool {
	hint := "[y/N]"
	if defYes {
		hint = "[Y/n]"
	}
	fmt.Printf("%s %s ", prompt, hint)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return defYes
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans == "" {
		return defYes
	}
	return ans == "y" || ans == "yes"
}

func modeLabel(m string) string {
	if strings.EqualFold(m, "server") {
		return "服务器模式"
	}
	return "本机模式"
}

func envStr(def string, keys ...string) string {
	return env(def, keys...)
}

func formatDurationBrief(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Seconds())
	if sec < 60 {
		return fmt.Sprintf("%d秒", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%d分%d秒", sec/60, sec%60)
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	if h < 24 {
		return fmt.Sprintf("%d小时%d分", h, m)
	}
	return fmt.Sprintf("%d天%d小时", h/24, h%24)
}
