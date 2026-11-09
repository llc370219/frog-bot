// frogbot：单个 AI 助理 Bot。所有配置都来自环境变量，见 README。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"frogbot/internal/bot"
	"frogbot/internal/onefrog"
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
	baseURL := env("https://api.x.ai/v1", "BASE_URL")
	key := env("", "API_KEY", "FROGBOT_API_KEY", "XAI_API_KEY", "OPENAI_API_KEY")
	agentModel := env("", "AGENT_MODEL")
	chatModel := env(agentModel, "CHAT_MODEL")
	if agentModel == "" || chatModel == "" {
		log.Fatal("请设置 AGENT_MODEL 和 CHAT_MODEL（以及 API_KEY、BASE_URL），见 README")
	}
	agentP := onefrog.NewOpenAI(baseURL, key)
	chatP := onefrog.NewOpenAI(env(baseURL, "CHAT_BASE_URL"), env(key, "CHAT_API_KEY"))
	var reviewer onefrog.Reviewer
	if m := env("", "REVIEWER_MODEL"); m != "" {
		reviewer = &onefrog.LLMReviewer{Provider: agentP, Model: m}
	}
	maxConc, _ := strconv.Atoi(env("4", "MAX_CONCURRENT"))
	taskMin, _ := strconv.Atoi(env("30", "TASK_MAX_MINUTES"))

	b, err := bot.New(bot.Config{
		Name:          env("Frog", "BOT_NAME"),
		DataDir:       env("./data", "DATA_DIR"),
		Workspace:     env("./workspace", "WORKSPACE"),
		ChatProvider:  chatP,
		ChatModel:     chatModel,
		AgentProvider: agentP,
		AgentModel:    agentModel,
		Reviewer:      reviewer,
		Policy:        onefrog.Policy(env("on-request", "POLICY")),
		ChatPolicy:    onefrog.Policy(env("untrusted", "CHAT_POLICY")),
		MaxConcurrent: maxConc,
		TaskMaxTime:   time.Duration(taskMin) * time.Minute,
		MasterKey:     os.Getenv("FROGBOT_MASTER_KEY"),
		Demo:          env("", "FROGBOT_DEMO") != "",
	})
	if err != nil {
		log.Fatal(err)
	}
	b.Start()
	addr := env("127.0.0.1:8080", "ADDR")
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
	srv := &http.Server{Addr: addr, Handler: b.Handler(webDir)}
	go func() {
		log.Printf("frog bot 已启动：http://%s （聊天模型 %s，agent 模型 %s）", addr, chatModel, agentModel)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	b.Close()
}
