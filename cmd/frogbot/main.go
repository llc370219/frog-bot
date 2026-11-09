// frogbot：单个 AI 助理 Bot。运行后在浏览器里打开管理页面，在网页里填模型设置即可；
// 也可以用环境变量配置（环境变量优先），见 README。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"frogbot/internal/bot"
	"frogbot/internal/onefrog"
	"frogbot/web"
)

// version 由发布脚本用 -ldflags "-X main.version=..." 写入。
var version = "dev"

func env(def string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return def
}

func main() {
	addrFlag := flag.String("addr", "", "监听地址（默认 127.0.0.1:8080，被占用时自动换下一个端口）；也可用 ADDR")
	dataFlag := flag.String("data", "", "数据目录（默认见 README）；也可用 DATA_DIR")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器；也可用 FROGBOT_NO_BROWSER=1")
	showVersion := flag.Bool("version", false, "显示版本号")
	flag.Parse()
	if *showVersion {
		fmt.Println("frogbot", version)
		return
	}
	log.SetFlags(log.Ldate | log.Ltime)

	dataDir, workspace := dirs(*dataFlag)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		fatal("创建数据目录失败: %v", err)
	}

	me := bot.ModelEnv{
		BaseURL:     env("", "BASE_URL"),
		APIKey:      env("", "API_KEY", "FROGBOT_API_KEY", "XAI_API_KEY", "OPENAI_API_KEY"),
		AgentModel:  env("", "AGENT_MODEL"),
		ChatModel:   env("", "CHAT_MODEL"),
		ChatBaseURL: env("", "CHAT_BASE_URL"),
		ChatAPIKey:  env("", "CHAT_API_KEY"),
	}
	if me.BaseURL == "" && (me.AgentModel != "" || me.ChatModel != "") {
		me.BaseURL = "https://api.x.ai/v1" // 兼容旧版：只设了模型名时默认 xAI
	}
	maxConc, _ := strconv.Atoi(env("4", "MAX_CONCURRENT"))
	taskMin, _ := strconv.Atoi(env("30", "TASK_MAX_MINUTES"))

	b, err := bot.New(bot.Config{
		Name:          env("Frog", "BOT_NAME"),
		DataDir:       dataDir,
		Workspace:     workspace,
		ModelEnv:      me,
		ReviewerModel: env("", "REVIEWER_MODEL"),
		Policy:        onefrog.Policy(env("on-request", "POLICY")),
		ChatPolicy:    onefrog.Policy(env("untrusted", "CHAT_POLICY")),
		MaxConcurrent: maxConc,
		TaskMaxTime:   time.Duration(taskMin) * time.Minute,
		MasterKey:     os.Getenv("FROGBOT_MASTER_KEY"),
		Demo:          env("", "FROGBOT_DEMO") != "",
		LocalOnly:     env("", "FROGBOT_ALLOW_REMOTE") == "",
	})
	if err != nil {
		fatal("%v", err)
	}

	var ui fs.FS
	if d := env("", "WEB_DIR"); d != "" {
		ui = os.DirFS(d)
	} else if emb, ok := web.Dist(); ok {
		ui = emb
	} else {
		ui = os.DirFS("web/dist") // 开发时：没打包前端就读磁盘
	}

	ln, addr, err := listen(env(*addrFlag, "ADDR"))
	if err != nil {
		fatal("%v", err)
	}
	b.Start()
	srv := &http.Server{Handler: b.HandlerFS(ui), ReadHeaderTimeout: 10 * time.Second}
	url := "http://" + browserHost(addr)
	chat, agent := b.ModelNames()
	fmt.Printf("\n  🐸 frog bot %s 已启动\n\n  打开管理页面： %s\n  数据目录：     %s\n  工作区：       %s\n", version, url, abs(dataDir), abs(workspace))
	if b.Configured() {
		fmt.Printf("  模型：         聊天 %s，后台 agent %s\n", chat, agent)
	} else {
		fmt.Printf("  模型：         还没配置，请在网页右上角「设置」里填写\n")
	}
	fmt.Printf("\n  只在本机使用，没有登录功能，不要暴露到公网。按 Ctrl+C 退出。\n\n")
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fatal("%v", err)
		}
	}()
	if !*noBrowser && env("", "FROGBOT_NO_BROWSER") == "" {
		go openBrowser(url)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	fmt.Println("\n正在退出…")
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	b.Close()
}

// dirs 决定数据目录和工作区：
//  1. -data / DATA_DIR（工作区默认是它旁边的 workspace，或 WORKSPACE）
//  2. 当前目录下已有 ./data（旧版从源码运行的方式）就继续用 ./data 和 ./workspace
//  3. 否则用系统的用户配置目录：
//     Windows %AppData%\frogbot，macOS ~/Library/Application Support/frogbot，Linux ~/.config/frogbot
func dirs(flagData string) (data, workspace string) {
	data = env(flagData, "DATA_DIR")
	if data != "" {
		return data, env(filepath.Join(filepath.Dir(filepath.Clean(data)), "workspace"), "WORKSPACE")
	}
	if st, err := os.Stat("data"); err == nil && st.IsDir() {
		return "data", env("workspace", "WORKSPACE")
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base, _ = os.UserHomeDir()
	}
	root := filepath.Join(base, "frogbot")
	return filepath.Join(root, "data"), env(filepath.Join(root, "workspace"), "WORKSPACE")
}

// listen 默认监听 127.0.0.1:8080；没指定地址且端口被占用时依次试 8081…8099。
func listen(addr string) (net.Listener, string, error) {
	if addr != "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, "", fmt.Errorf("监听地址格式不对（例如 127.0.0.1:8080）: %v", err)
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			log.Printf("⚠ 警告：监听 %s 不是本机地址。frog bot 没有登录功能，别人能连上就能操作你的电脑。", addr)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, "", fmt.Errorf("无法监听 %s: %v", addr, err)
		}
		return ln, ln.Addr().String(), nil
	}
	var last error
	for port := 8080; port < 8100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, ln.Addr().String(), nil
		}
		last = err
	}
	return nil, "", fmt.Errorf("8080-8099 端口都被占用了，用 -addr 127.0.0.1:端口 指定一个: %v", last)
}

func browserHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return // 没有图形界面（服务器 / SSH），只打印地址
		}
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		if !errors.Is(err, exec.ErrNotFound) {
			log.Printf("没能自动打开浏览器（%v），请手动打开 %s", err, url)
		}
		return
	}
	go cmd.Wait()
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// fatal 打印错误后退出。Windows 上双击运行时窗口会立刻关掉，所以先等用户按回车。
func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "\n错误："+format+"\n", a...)
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "按回车键退出…")
		var s string
		fmt.Scanln(&s)
	}
	os.Exit(1)
}
