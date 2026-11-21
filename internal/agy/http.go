package agy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frogbot/internal/onefrog"
)

const maxBody = 1 << 20

// Handler 暴露 /api/agy/... 路由（直连 agy 状态、项目文件夹、对话会话、实时流与权限确认）。
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/agy/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.Status(r.Context()), nil)
	})

	mux.HandleFunc("GET /api/agy/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.ListProjects(), nil)
	})

	mux.HandleFunc("POST /api/agy/projects", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if !decodeJSON(w, r, &body, 8192) {
			return
		}
		p, err := h.CreateProject(body.Name, body.Path)
		writeJSON(w, p, err)
	})

	mux.HandleFunc("DELETE /api/agy/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		delFiles := r.URL.Query().Get("delete_files") == "1"
		err := h.DeleteProject(r.PathValue("id"), delFiles)
		writeJSON(w, map[string]bool{"ok": true}, err)
	})

	mux.HandleFunc("GET /api/agy/projects/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		proj, ok := h.FindProject(r.PathValue("id"))
		if !ok {
			http.Error(w, "项目不存在", http.StatusNotFound)
			return
		}
		serveProjectFile(w, r, proj.Path)
	})

	mux.HandleFunc("GET /api/agy/sessions", func(w http.ResponseWriter, r *http.Request) {
		list, err := h.ListSessions(r.Context(), r.URL.Query().Get("project_id"))
		writeJSON(w, list, err)
	})

	mux.HandleFunc("POST /api/agy/sessions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProjectID string `json:"project_id"`
			Mode      string `json:"mode"`
			Model     string `json:"model"`
		}
		if !decodeJSON(w, r, &body, 8192) {
			return
		}
		snap, err := h.CreateSession(r.Context(), body.ProjectID, body.Mode, body.Model)
		writeJSON(w, snap, err)
	})

	mux.HandleFunc("POST /api/agy/sessions/{id}/config", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode  string `json:"mode"`
			Model string `json:"model"`
			Title string `json:"title"`
		}
		if !decodeJSON(w, r, &body, 8192) {
			return
		}
		sum, err := h.UpdateSessionConfig(r.Context(), r.PathValue("id"), body.Mode, body.Model, body.Title)
		writeJSON(w, sum, err)
	})

	mux.HandleFunc("DELETE /api/agy/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		err := h.DeleteSession(r.Context(), r.PathValue("id"))
		writeJSON(w, map[string]bool{"ok": true}, err)
	})

	mux.HandleFunc("POST /api/agy/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProjectID string `json:"project_id"`
			Text      string `json:"text"`
		}
		if !decodeJSON(w, r, &body, maxBody) {
			return
		}
		m, err := h.SendPrompt(r.Context(), r.PathValue("id"), body.ProjectID, body.Text)
		writeJSON(w, m, err)
	})

	mux.HandleFunc("POST /api/agy/sessions/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		err := h.StopPrompt(r.PathValue("id"))
		writeJSON(w, map[string]bool{"ok": true}, err)
	})

	mux.HandleFunc("POST /api/agy/sessions/{id}/permissions/{permId}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OptionID string `json:"option_id"`
		}
		if !decodeJSON(w, r, &body, 4096) {
			return
		}
		err := h.DecidePermission(r.PathValue("id"), r.PathValue("permId"), body.OptionID)
		writeJSON(w, map[string]bool{"ok": true}, err)
	})

	mux.HandleFunc("GET /api/agy/sessions/{id}/stream", h.serveStream)
	return mux
}

func (h *Hub) serveStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	snap, live, unsub, err := h.Subscribe(r.Context(), r.PathValue("id"), r.URL.Query().Get("project_id"))
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func(ev StreamEvent) {
		if r.Context().Err() != nil {
			return
		}
		if ev.Seq > 0 {
			fmt.Fprintf(w, "id: %d\n", ev.Seq)
		}
		raw, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}

	send(StreamEvent{Kind: "snapshot", Time: time.Now(), Data: snap})
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case ev, open := <-live:
			if !open {
				return
			}
			send(ev)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func serveProjectFile(w http.ResponseWriter, r *http.Request, root string) {
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		// 未传 path 时列出该目录第一层或指定子目录的内容，方便界面浏览项目文件夹
		dir := root
		if sub := strings.TrimSpace(r.URL.Query().Get("dir")); sub != "" {
			var err error
			dir, err = onefrog.ResolvePath(root, sub)
			if err != nil {
				writeJSON(w, nil, err)
				return
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		type item struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
			Size  int64  `json:"size,omitempty"`
		}
		out := make([]item, 0, len(entries))
		for _, e := range entries {
			if e.Name() == ".git" || e.Name() == "node_modules" {
				continue
			}
			var sz int64
			if info, err := e.Info(); err == nil && !e.IsDir() {
				sz = info.Size()
			}
			out = append(out, item{Name: e.Name(), IsDir: e.IsDir(), Size: sz})
		}
		writeJSON(w, out, nil)
		return
	}
	p, err := onefrog.ResolvePath(root, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		http.Error(w, "文件不存在", http.StatusNotFound)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, "打不开文件", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if strings.HasPrefix(ct, "text/html") || strings.Contains(ct, "svg") || strings.Contains(ct, "javascript") {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	disp := "inline"
	if r.URL.Query().Get("download") != "" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": filepath.Base(p)}))
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, nil, errors.New("请求格式无效"))
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, nil, errors.New("请求包含多余内容"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		v = map[string]string{"error": err.Error()}
	}
	_ = json.NewEncoder(w).Encode(v)
}
