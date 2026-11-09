// frogbot：多个独立的 AI 助理 Bot，在浏览器里管理。
//
//	本机模式（默认）：frogbot            只监听 127.0.0.1，自动打开浏览器，没有登录。
//	服务器模式：      frogbot -server    监听 0.0.0.0:8080，必须用用户名和密码登录。
//	重置登录密码：    frogbot reset-password [-data 目录] [-user 用户名] [-password-stdin]
//
// 模型在网页的「设置」里配置；也可以用环境变量（优先于网页设置），见 README。
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"frogbot/internal/auth"
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
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		resetPassword(os.Args[2:])
		return
	}
	addrFlag := flag.String("addr", "", "监听地址，例如 127.0.0.1:8080（本机模式默认 127.0.0.1:8080，被占用时自动换下一个端口；服务器模式默认 0.0.0.0:8080）；也可用 ADDR")
	portFlag := flag.Int("port", 0, "只改端口（服务器模式常用）；也可用 PORT")
	serverFlag := flag.Bool("server", false, "服务器模式：监听所有网卡，必须登录；也可用 FROGBOT_MODE=server")
	dataFlag := flag.String("data", "", "数据目录（默认见 README）；也可用 DATA_DIR")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器；也可用 FROGBOT_NO_BROWSER=1")
	showVersion := flag.Bool("version", false, "显示版本号")
	flag.Parse()
	if *showVersion {
		fmt.Println("frogbot", version)
		return
	}
	if version != "dev" {
		bot.Version = version
	}
	log.SetFlags(log.Ldate | log.Ltime)
	server := *serverFlag || strings.EqualFold(env("", "FROGBOT_MODE"), "server")

	dataDir, workspace := dirs(*dataFlag)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		fatal("创建数据目录失败: %v", err)
	}

	var gate *auth.Auth
	var firstPassword string
	if server {
		var err error
		if gate, firstPassword, err = openAuth(dataDir); err != nil {
			fatal("%v", err)
		}
	}

	baseURL := env("", "BASE_URL")
	key := env("", "API_KEY", "FROGBOT_API_KEY", "XAI_API_KEY", "OPENAI_API_KEY")
	agentModel := env("", "AGENT_MODEL")
	chatModel := env(agentModel, "CHAT_MODEL")
	if baseURL == "" && chatModel != "" {
		baseURL = "https://api.x.ai/v1" // 兼容旧版：只设了模型名时默认 xAI
	}
	maxConc, _ := strconv.Atoi(env("4", "MAX_CONCURRENT"))
	taskMin, _ := strconv.Atoi(env("30", "TASK_MAX_MINUTES"))
	ctxTokens, _ := strconv.Atoi(env("60000", "CONTEXT_TOKENS"))

	bots, err := bot.OpenBots(bot.Config{
		Name:           env("Frog", "BOT_NAME"),
		DataDir:        dataDir,
		Workspace:      workspace,
		ChatModel:      chatModel,
		AgentModel:     agentModel,
		ReviewerModel:  env("", "REVIEWER_MODEL"),
		Policy:         onefrog.Policy(env("on-request", "POLICY")),
		ChatPolicy:     onefrog.Policy(env("untrusted", "CHAT_POLICY")),
		MaxConcurrent:  maxConc,
		TaskMaxTime:    time.Duration(taskMin) * time.Minute,
		ContextTokens:  ctxTokens,
		MasterKey:      os.Getenv("FROGBOT_MASTER_KEY"),
		Demo:           env("", "FROGBOT_DEMO") != "",
		InitialBaseURL: baseURL,
		InitialKey:     key,
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
		ui = os.DirFS("web/dist") // 开发时：没打包前端就读磁盘上的 web/dist
	}

	addr := env(*addrFlag, "ADDR")
	port := *portFlag
	if port == 0 {
		port, _ = strconv.Atoi(env("0", "PORT"))
	}
	ln, addr, err := listen(addr, port, server)
	if err != nil {
		bots.Close()
		fatal("%v", err)
	}
	handler := bots.Handler(ui)
	if server {
		handler = gate.Middleware(handler)
	} else {
		handler = bot.LocalOnly(handler)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}

	page := "http://" + browserHost(addr)
	fmt.Printf("\n  🐸 frog bot %s 已启动（%s）\n\n", version, map[bool]string{true: "服务器模式", false: "本机模式"}[server])
	fmt.Printf("  管理页面： %s\n  数据目录： %s\n  工作区：   %s\n", page, absPath(dataDir), absPath(workspace))
	if server {
		fmt.Printf("  用户名：   %s\n", gate.Username())
		if firstPassword != "" {
			fmt.Printf("  初始密码： %s   （只显示这一次，请马上记下；忘了用 frogbot reset-password 重置）\n", firstPassword)
		}
		fmt.Printf("\n  从外网访问用 http://<服务器公网 IP>:%s 。HTTP 是明文，强烈建议配域名 + HTTPS（见 README）。\n", portOf(addr))
		fmt.Printf("  后台 agent 能在这台机器上执行命令（没有沙箱），请用专门的服务器。\n\n")
	} else {
		fmt.Printf("\n  只在本机使用，没有登录功能。要从别的电脑访问请用 frogbot -server。按 Ctrl+C 退出。\n\n")
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fatal("%v", err)
		}
	}()
	if !server && !*noBrowser && env("", "FROGBOT_NO_BROWSER") == "" {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := openBrowser(page); err != nil && !errors.Is(err, errNoDisplay) {
				log.Printf("没能自动打开浏览器（%v），请手动打开 %s", err, page)
			}
		}()
	}
	<-ctx.Done()
	fmt.Println("\n正在退出…")
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	bots.Close()
}

// openAuth 读取服务器模式的登录账号；还没有账号时（例如没用安装脚本、直接 frogbot -server）
// 自动创建 FROGBOT_USER（默认 admin）和随机密码，并返回密码让启动信息显示一次。
func openAuth(dataDir string) (*auth.Auth, string, error) {
	if !auth.Exists(dataDir) {
		user := env("admin", "FROGBOT_USER")
		pw := os.Getenv("FROGBOT_PASSWORD")
		shown := ""
		if pw == "" {
			pw = auth.RandomPassword()
			shown = pw
		}
		if err := auth.SetPassword(dataDir, user, pw); err != nil {
			return nil, "", fmt.Errorf("创建登录账号失败: %w", err)
		}
		if shown != "" {
			log.Printf("已创建登录账号 %s，初始密码见下方启动信息（只显示一次）", user)
		}
		a, err := auth.Open(dataDir)
		return a, shown, err
	}
	a, err := auth.Open(dataDir)
	return a, "", err
}

// resetPassword：frogbot reset-password [-data DIR] [-user NAME] [-password-stdin]
// 不带 -password-stdin 时生成随机密码并打印。旧的登录会话全部失效。
func resetPassword(args []string) {
	fl := flag.NewFlagSet("reset-password", flag.ExitOnError)
	data := fl.String("data", "", "数据目录（默认同主程序：DATA_DIR 或默认位置）")
	user := fl.String("user", "", "用户名（默认保持原来的，没有账号时用 admin）")
	fromStdin := fl.Bool("password-stdin", false, "从标准输入读新密码（一行），不打印")
	quiet := fl.Bool("quiet", false, "只输出密码本身（给脚本用）")
	_ = fl.Parse(args)
	dataDir, _ := dirs(*data)
	name := *user
	if name == "" {
		if a, err := auth.Open(dataDir); err == nil {
			name = a.Username()
		} else {
			name = env("admin", "FROGBOT_USER")
		}
	}
	pw := ""
	if *fromStdin {
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, 4096)).ReadString('\n')
		if err != nil && line == "" {
			fatal("没有从标准输入读到密码")
		}
		pw = strings.TrimRight(line, "\r\n")
	} else {
		pw = auth.RandomPassword()
	}
	if err := auth.SetPassword(dataDir, name, pw); err != nil {
		fatal("%v", err)
	}
	if *quiet {
		if !*fromStdin {
			fmt.Println(pw)
		}
		return
	}
	fmt.Printf("已重置登录密码（旧的登录全部失效）。\n  数据目录：%s\n  用户名：  %s\n", absPath(dataDir), name)
	if !*fromStdin {
		fmt.Printf("  新密码：  %s\n", pw)
	}
	fmt.Println("正在运行的 frog bot 会自动用新密码，不需要重启。")
}

// dirs 决定数据目录和工作区：
//  1. -data / DATA_DIR（工作区默认是它旁边的 workspace，或 WORKSPACE）
//  2. 当前目录下已有 ./data（从源码运行的方式）就继续用 ./data 和 ./workspace
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

// listen：指定了地址就用它；服务器模式默认 0.0.0.0:8080（或 PORT）；
// 本机模式默认 127.0.0.1:8080，被占用时依次试 8081…8099。
func listen(addr string, port int, server bool) (net.Listener, string, error) {
	if addr == "" && (server || port != 0) {
		host := "127.0.0.1"
		if server {
			host = "0.0.0.0"
		}
		if port == 0 {
			port = 8080
		}
		addr = net.JoinHostPort(host, strconv.Itoa(port))
	}
	if addr != "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, "", fmt.Errorf("监听地址格式不对（例如 127.0.0.1:8080）: %v", err)
		}
		if ip := net.ParseIP(host); !server && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			log.Printf("⚠ 本机模式只接受发往 127.0.0.1 / localhost 的请求；监听 %s 也没法从别的电脑打开。要远程使用请加 -server（需要登录）。", addr)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, "", fmt.Errorf("无法监听 %s: %v", addr, err)
		}
		return ln, ln.Addr().String(), nil
	}
	var last error
	for p := 8080; p < 8100; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
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
	return (&url.URL{Host: net.JoinHostPort(host, port)}).Host
}

func portOf(addr string) string {
	_, port, _ := net.SplitHostPort(addr)
	return port
}

func absPath(p string) string {
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
