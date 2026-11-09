package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Handler 返回 HTTP 接口（以及可选的前端静态文件）。
//
//	GET  /api/info                 Bot 名字、模型等
//	GET  /api/stream               SSE：所有事件（支持 Last-Event-ID 断线续传）
//	POST /api/messages             {"text":"..."}
//	POST /api/stop                 停止所有任务（确定性，不经过模型）
//	POST /api/approvals/{id}       {"decision":"once|session|deny"}  —— 只能由用户点按钮触发
//	POST /api/cards/{id}           {"answer":"..."} 或 {"dismiss":true}
//	POST /api/regenerate           重新生成最后一条回复
//	POST /api/messages/{id}/edit   编辑并重发最后一条用户消息
//	POST /api/messages/{id}/react  {"emoji":"👍"}（再点一次取消）
//	POST /api/uploads              multipart 上传附件（存进工作区 uploads/）
//	GET  /api/files?path=…         打开/下载工作区里的文件（&download=1 下载）
//	POST /api/tasks/{id}/{action}  pause | resume | cancel | retry | top | up
//	GET  /api/secrets              密钥列表（只有名字、说明、时间，永远没有值）
//	POST /api/secrets              {"request_id":"sec_…","value":"…"}（或 {"name","value","description"}）
//	                               值只经过这一个接口，直接加密落盘，不进聊天记录/事件/日志
//	GET  /api/schedules             定时计划列表
//	GET  /api/schedules/{id}         单个计划
//	POST /api/schedules/{id}/{action} pause|resume|run|delete（界面按钮；创建/大改仍走聊天层审批）
//	POST /api/secrets/requests/{id}/cancel   取消密钥卡片（= 拒绝提供）
func (b *Bot) Handler(webDir string, embeddedFS ...fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.GetSettings(), nil)
	})
	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		payload, err := readAPISettings(w, r)
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		saved, err := b.SaveSettings(payload)
		writeJSON(w, saved, err)
	})
	mux.HandleFunc("POST /api/settings/models", func(w http.ResponseWriter, r *http.Request) {
		p, err := readAPISettings(w, r)
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		models, err := b.FetchRemoteModels(r.Context(), p)
		writeJSON(w, map[string]any{"models": models}, err)
	})
	mux.HandleFunc("POST /api/settings/test", func(w http.ResponseWriter, r *http.Request) {
		p, err := readAPISettings(w, r)
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		latency, err := b.TestConnection(r.Context(), p)
		writeJSON(w, map[string]any{"latency_ms": latency}, err)
	})
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		status, err := checkUpdate(r.Context(), releaseClient, latestReleaseAPI, currentVersion())
		writeJSON(w, status, err)
	})
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		_, chatModel := b.resolveModel(true)
		_, agentModel := b.resolveModel(false)
		writeJSON(w, map[string]any{"name": b.botProfile().Name, "chat_model": chatModel, "agent_model": agentModel,
			"policy": b.cfg.Policy, "demo": b.cfg.Demo, "max_concurrent": b.cfg.MaxConcurrent,
			"stall_sec": int(b.cfg.StallTimeout.Seconds()), "timezone": "Asia/Shanghai", "version": currentVersion()}, nil)
	})
	mux.HandleFunc("POST /api/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text  string
			Files []Attachment
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m, err := b.Send(body.Text, body.Files...)
		writeJSON(w, m, err)
	})
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") == "reply" { // Esc：只停聊天层正在写的回复
			writeJSON(w, map[string]bool{"stopped": b.StopReply()}, nil)
			return
		}
		writeJSON(w, map[string]int{"stopped": b.Stop()}, nil)
	})
	mux.HandleFunc("POST /api/regenerate", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true}, b.Regenerate())
	})
	mux.HandleFunc("POST /api/messages/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		m, err := b.EditLast(r.PathValue("id"), body.Text)
		writeJSON(w, m, err)
	})
	mux.HandleFunc("POST /api/messages/{id}/react", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Emoji string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, map[string]bool{"ok": true}, b.React(r.PathValue("id"), body.Emoji))
	})
	mux.HandleFunc("POST /api/uploads", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<20)
		mr, err := r.MultipartReader()
		if err != nil {
			writeJSON(w, nil, fmt.Errorf("需要 multipart 上传"))
			return
		}
		var out []Attachment
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			if part.FileName() == "" {
				continue
			}
			a, err := b.Upload(part.FileName(), part)
			if err != nil {
				writeJSON(w, nil, err)
				return
			}
			out = append(out, a)
		}
		writeJSON(w, out, nil)
	})
	mux.HandleFunc("GET /api/files", b.serveFile)
	mux.HandleFunc("POST /api/tasks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		msg, err := b.TaskAction(r.PathValue("id"), r.PathValue("action"))
		writeJSON(w, map[string]string{"message": msg}, err)
	})
	mux.HandleFunc("POST /api/approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Decision string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, map[string]bool{"ok": true}, b.Decide(r.PathValue("id"), body.Decision))
	})
	mux.HandleFunc("POST /api/cards/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answer  string
			Dismiss bool
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, map[string]bool{"ok": true}, b.AnswerCard(r.PathValue("id"), body.Answer, body.Dismiss))
	})
	mux.HandleFunc("GET /api/secrets", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.vault.List(), nil)
	})
	mux.HandleFunc("POST /api/secrets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RequestID   string `json:"request_id"`
			Name        string `json:"name"`
			Value       string `json:"value"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeJSON(w, nil, fmt.Errorf("请求格式不对"))
			return
		}
		name, err := b.SaveSecret(body.RequestID, body.Name, body.Value, body.Description)
		body.Value = ""
		writeJSON(w, map[string]any{"ok": true, "name": name}, err)
	})
	mux.HandleFunc("POST /api/secrets/requests/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true}, b.CancelSecret(r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/schedules", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.Schedules(), nil)
	})
	mux.HandleFunc("GET /api/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := b.findSchedule(r.PathValue("id"))
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		writeJSON(w, p.Public(), nil)
	})
	mux.HandleFunc("POST /api/schedules/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id, action := r.PathValue("id"), r.PathValue("action")
		p, err := b.findSchedule(id)
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		switch action {
		case "pause":
			p2, err := b.sched.SetEnabled(p.ID, false)
			writeJSON(w, p2.Public(), err)
		case "resume":
			p2, err := b.sched.SetEnabled(p.ID, true)
			writeJSON(w, p2.Public(), err)
		case "run":
			c, err := b.sched.RunNow(p.ID)
			writeJSON(w, map[string]any{"claim": c, "task_id": c.TaskID}, err)
		case "delete":
			writeJSON(w, map[string]bool{"ok": true}, b.sched.Remove(p.ID))
		default:
			writeJSON(w, nil, fmt.Errorf("未知操作 %s（pause|resume|run|delete）", action))
		}
	})
	mux.HandleFunc("GET /api/stream", b.serveStream)
	if len(embeddedFS) > 0 && embeddedFS[0] != nil {
		mux.Handle("/", spaFS(embeddedFS[0], webDir))
	} else if webDir != "" {
		mux.Handle("/", spa(webDir))
	}
	return mux
}

func (b *Bot) serveStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	last, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if v := r.URL.Query().Get("after"); v != "" && last == 0 {
		last, _ = strconv.ParseInt(v, 10, 64)
	}
	b.turnMu.Lock()
	live, unsub := b.store.Subscribe() // 先订阅再补发历史，避免漏事件
	busy := b.turnCancel != nil
	b.turnMu.Unlock()
	defer unsub()
	send := func(e Event) {
		if e.Seq > 0 {
			if e.Seq <= last {
				return
			}
			last = e.Seq
			fmt.Fprintf(w, "id: %d\n", e.Seq)
		}
		data, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b.store.Scrub(data)) // 历史事件可能早于某个密钥的保存，推送前再脱敏一次
	}
	for _, e := range b.store.Since(last) {
		send(e)
	}
	busyData, _ := json.Marshal(map[string]bool{"busy": busy})
	send(Event{Kind: "replay.done", Time: time.Now(), Data: busyData})
	flusher.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case e, ok := <-live:
			if !ok {
				return // 太慢被断开，浏览器会自动重连补齐
			}
			if !e.Internal {
				send(e)
				flusher.Flush()
			}
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-b.ctx.Done():
			return
		}
	}
}

func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintln(w, "前端还没构建：cd web && npm install && npm run build（开发时用 npm run dev）")
			return
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.Clean("/"+r.URL.Path))); err != nil {
			http.ServeFile(w, r, index)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func spaFS(efs fs.FS, fallbackDir string) http.Handler {
	if fallbackDir != "" {
		if _, err := os.Stat(filepath.Join(fallbackDir, "index.html")); err == nil {
			return spa(fallbackDir)
		}
	}
	sub, err := fs.Sub(efs, "dist")
	if err != nil {
		sub = efs
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// fs.FS uses slash-delimited paths on every platform, including Windows.
		clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if clean == "" || clean == "." {
			clean = "index.html"
		}
		if _, err := fs.Stat(sub, clean); err != nil {
			idx, err := sub.Open("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer idx.Close()
			stat, _ := idx.Stat()
			http.ServeContent(w, r, "index.html", stat.ModTime(), idx.(io.ReadSeeker))
			return
		}
		files.ServeHTTP(w, r)
	})
}

func readAPISettings(w http.ResponseWriter, r *http.Request) (APISettings, error) {
	var s APISettings
	// Cross-origin HTML forms must not be able to change or probe credentials.
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		return s, fmt.Errorf("请求必须使用 JSON")
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&s); err != nil {
		return s, fmt.Errorf("请求格式不正确: %w", err)
	}
	return s, nil
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(400)
		v = map[string]string{"error": err.Error()}
	}
	_ = json.NewEncoder(w).Encode(v)
}
