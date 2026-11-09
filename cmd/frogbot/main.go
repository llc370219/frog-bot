// frogbot：支持多个独立 AI 助理，可在网页中配置模型 API。
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"frogbot/internal/bot"
	"frogbot/internal/onefrog"
	"frogbot/web"
)

func env(def string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return def
}

func main() {
	addr := env("127.0.0.1:8080", "ADDR")
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("无法监听 %s: %v", addr, err)
	}
	defer listener.Close()

	baseURL := env("", "BASE_URL")
	key := env("", "API_KEY", "FROGBOT_API_KEY", "XAI_API_KEY", "OPENAI_API_KEY")
	agentModel := env("", "AGENT_MODEL")
	chatModel := env(agentModel, "CHAT_MODEL")
	if baseURL == "" && (key != "" || chatModel != "") {
		baseURL = "https://api.x.ai/v1"
	}
	maxConc, _ := strconv.Atoi(env("4", "MAX_CONCURRENT"))
	taskMin, _ := strconv.Atoi(env("30", "TASK_MAX_MINUTES"))

	b, err := bot.OpenBots(bot.Config{
		Name:           env("Frog", "BOT_NAME"),
		DataDir:        env("./data", "DATA_DIR"),
		Workspace:      env("./workspace", "WORKSPACE"),
		ChatModel:      chatModel,
		AgentModel:     agentModel,
		ReviewerModel:  env("", "REVIEWER_MODEL"),
		Policy:         onefrog.Policy(env("on-request", "POLICY")),
		ChatPolicy:     onefrog.Policy(env("untrusted", "CHAT_POLICY")),
		MaxConcurrent:  maxConc,
		TaskMaxTime:    time.Duration(taskMin) * time.Minute,
		MasterKey:      os.Getenv("FROGBOT_MASTER_KEY"),
		Demo:           env("", "FROGBOT_DEMO") != "",
		InitialBaseURL: baseURL,
		InitialKey:     key,
	})
	if err != nil {
		log.Fatal(err)
	}
	webDir := env("", "WEB_DIR")
	if webDir == "" {
		if _, err := os.Stat("web/dist"); err == nil {
			webDir = "web/dist"
		} else if _, err := os.Stat("frog-bot/web/dist"); err == nil {
			webDir = "frog-bot/web/dist"
		} else {
			webDir = "web/dist"
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv := &http.Server{Addr: addr, Handler: b.Handler(webDir, web.DistFS),
		BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		if err := srv.Serve(listener); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	switch host {
	case "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	page := (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String()
	log.Printf("Frog Bot 已启动：%s", page)
	if env("", "FROGBOT_NO_BROWSER") == "" {
		go func() {
			if err := openBrowser(page); err != nil {
				log.Printf("无法自动打开浏览器：%v。请手动打开 %s", err, page)
			}
		}()
	}
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	b.Close()
}
