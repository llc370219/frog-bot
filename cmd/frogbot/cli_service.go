package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	macLaunchAgentLabel = "io.github.llc370219.frogbot"
	winTaskName         = "FrogBot"
)

func macPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", macLaunchAgentLabel+".plist")
}

func linuxUserUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "frogbot.service")
}

func serviceStatusSummary(env cliEnv) (installed bool, detail string) {
	switch runtime.GOOS {
	case "linux":
		if env.isSystemd {
			out, _ := exec.Command("systemctl", "is-enabled", "frogbot").Output()
			state := strings.TrimSpace(string(out))
			if state == "" {
				state = "installed"
			}
			return true, fmt.Sprintf("systemd 系统服务（/etc/systemd/system/frogbot.service，开机自启：%s）", state)
		}
		if _, err := os.Stat(linuxUserUnitPath()); err == nil {
			out, _ := exec.Command("systemctl", "--user", "is-enabled", "frogbot").Output()
			state := strings.TrimSpace(string(out))
			if state == "" {
				state = "installed"
			}
			return true, fmt.Sprintf("systemd 用户服务（%s，开机自启：%s）", linuxUserUnitPath(), state)
		}
		return false, "未配置开机自启（运行 frog service install 即可开启）"

	case "darwin":
		p := macPlistPath()
		if _, err := os.Stat(p); err == nil {
			return true, fmt.Sprintf("macOS LaunchAgent（%s）", p)
		}
		return false, "未配置开机自启（运行 frog service install 即可开启）"

	case "windows":
		if exec.Command("schtasks", "/Query", "/TN", winTaskName).Run() == nil {
			return true, fmt.Sprintf("Windows 计划任务（%s，登录后自动后台运行）", winTaskName)
		}
		return false, "未配置开机自启（运行 frog service install 即可开启）"
	}
	return false, "当前系统不支持自动配置服务"
}

func installAutostartService(env cliEnv, cfg cliConfig) error {
	exe, err := resolveSelfExecutable()
	if err != nil {
		return err
	}
	args := buildServeArgs(env, cfg)

	switch runtime.GOOS {
	case "linux":
		if env.isSystemd {
			if err := runCmdPassthrough("systemctl", "enable", "--now", "frogbot"); err != nil {
				return fmt.Errorf("启用 systemd 系统服务失败（可能需要 sudo）：%w", err)
			}
			return nil
		}
		unitPath := linuxUserUnitPath()
		if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
			return err
		}
		execLine := shellQuote(exe)
		for _, a := range args {
			execLine += " " + shellQuote(a)
		}
		unit := fmt.Sprintf(`[Unit]
Description=frog (user service)
After=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
`, execLine)
		if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		if err := runCmdPassthrough("systemctl", "--user", "enable", "--now", "frogbot"); err != nil {
			return fmt.Errorf("启用用户级 systemd 服务失败：%w", err)
		}
		return nil

	case "darwin":
		p := macPlistPath()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(env.logFile), 0o755); err != nil {
			return err
		}
		var argXML strings.Builder
		fmt.Fprintf(&argXML, "    <string>%s</string>\n", xmlEscape(exe))
		for _, a := range args {
			fmt.Fprintf(&argXML, "    <string>%s</string>\n", xmlEscape(a))
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, macLaunchAgentLabel, argXML.String(), xmlEscape(env.logFile), xmlEscape(env.logFile))
		if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
			return err
		}
		uid := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", uid+"/"+macLaunchAgentLabel).Run()
		if err := exec.Command("launchctl", "bootstrap", uid, p).Run(); err != nil {
			// 兼容旧版 macOS launchctl
			if err2 := exec.Command("launchctl", "load", "-w", p).Run(); err2 != nil {
				return fmt.Errorf("加载 LaunchAgent 失败：%v / %v", err, err2)
			}
		}
		return nil

	case "windows":
		// 使用当前用户登录后自动触发的计划任务（无需管理员 UAC），调用 frog start 拉起无黑框后台守护进程
		var cmdLine strings.Builder
		fmt.Fprintf(&cmdLine, `"%s" start -data "%s"`, exe, env.dataDir)
		out, err := exec.Command("schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", winTaskName, "/TR", cmdLine.String()).CombinedOutput()
		if err != nil {
			return fmt.Errorf("创建 Windows 开机自启任务失败：%s（%w）", strings.TrimSpace(string(out)), err)
		}
		return nil

	default:
		return fmt.Errorf("当前系统 %s 暂不支持自动配置开机自启", runtime.GOOS)
	}
}

func uninstallAutostartService(env cliEnv) error {
	switch runtime.GOOS {
	case "linux":
		if env.isSystemd {
			return runCmdPassthrough("systemctl", "disable", "--now", "frogbot")
		}
		_ = exec.Command("systemctl", "--user", "disable", "--now", "frogbot").Run()
		_ = os.Remove(linuxUserUnitPath())
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return nil

	case "darwin":
		p := macPlistPath()
		uid := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", uid+"/"+macLaunchAgentLabel).Run()
		_ = exec.Command("launchctl", "unload", "-w", p).Run()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil

	case "windows":
		out, err := exec.Command("schtasks", "/Delete", "/F", "/TN", winTaskName).CombinedOutput()
		if err != nil && !strings.Contains(strings.ToLower(string(out)), "cannot find") && !strings.Contains(string(out), "找不到") {
			return fmt.Errorf("移除 Windows 计划任务失败：%s（%w）", strings.TrimSpace(string(out)), err)
		}
		return nil

	default:
		return fmt.Errorf("当前系统 %s 暂不支持", runtime.GOOS)
	}
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`!&*?;<>|()") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}
