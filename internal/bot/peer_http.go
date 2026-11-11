package bot

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"

	"frogbot/internal/onefrog"
)

// PeerHandler exposes Bot workspaces and the allowed configuration operations
// to paired devices. Account, Bot vault and approval decisions stay local.
func (bs *Bots) PeerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+peerPrefix+"/bots", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, bs.publicProfiles(), nil)
	})
	mux.HandleFunc("POST "+peerPrefix+"/bots", func(w http.ResponseWriter, r *http.Request) {
		var draft botDraft
		if err := readPeerJSON(w, r, &draft); err != nil {
			writeJSON(w, nil, err)
			return
		}
		profile, err := bs.setProfile("", BotProfile{Name: draft.Name, Instructions: draft.Instructions})
		profile.Instructions = ""
		writeJSON(w, profile, err)
	})
	mux.HandleFunc(peerPrefix+"/bots/{bot}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		id, rest := r.PathValue("bot"), r.PathValue("rest")
		if !validBotID(id) || !peerBotRoute(r.Method, rest) {
			http.NotFound(w, r)
			return
		}
		limitPeerBody(w, r, rest)
		bs.serveBotRoute(w, r, id, rest, true)
	})
	mux.HandleFunc("POST "+peerPrefix+"/inbox", bs.receivePeerMessage)
	bs.mountDeviceSettings(mux, peerPrefix, false)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := bs.network.find(r.Header.Get(deviceHeader))
		if !ok || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+p.Token)) != 1 {
			http.Error(w, "unrecognized device", http.StatusUnauthorized)
			return
		}
		w.Header().Set(deviceHeader, bs.network.id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

// Both sides use the same exact list; the device settings gateway never
// forwards account changes, approval decisions or arbitrary device actions.
var deviceSettingsRoutes = []struct{ method, endpoint string }{
	{"GET", "/settings"},
	{"POST", "/settings"},
	{"GET", "/settings/connections"},
	{"POST", "/settings/connections"},
	{"POST", "/settings/models"},
	{"POST", "/settings/test"},
	{"GET", "/update"},
	{"GET", "/channels"},
	{"POST", "/channels"},
	{"POST", "/channels/models"},
	{"POST", "/channels/test"},
}

func (bs *Bots) mountDeviceSettings(mux *http.ServeMux, prefix string, outbound bool) {
	for _, route := range deviceSettingsRoutes {
		mux.HandleFunc(route.method+" "+prefix+route.endpoint, func(w http.ResponseWriter, r *http.Request) {
			if outbound {
				bs.proxyDeviceSettings(w, r, route.endpoint)
			} else if route.method == "GET" && route.endpoint == "/channels" {
				writeJSON(w, bs.channelsView(), nil)
			} else {
				bs.serveBotRoute(w, r, defaultBotID, strings.TrimPrefix(route.endpoint, "/"), false)
			}
		})
	}
}

func (bs *Bots) proxyDeviceSettings(w http.ResponseWriter, r *http.Request, endpoint string) {
	n := bs.network
	peer, ok := n.find(r.PathValue("device"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body io.Reader
	headers := make(http.Header)
	if r.Method == http.MethodPost {
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if media != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		body = http.MaxBytesReader(w, r.Body, 1<<20)
		headers.Set("Content-Type", "application/json")
	}
	resp, err := n.doRequest(r.Context(), n.settingsClient, peer, r.Method, endpoint, body, headers)
	if err != nil {
		peerProxyError(w, err.Error())
		return
	}
	defer resp.Body.Close()
	proxyPeerJSON(w, resp)
}

type botDraft struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}

func (bs *Bots) proxyPeerCreateBot(w http.ResponseWriter, r *http.Request) {
	n := bs.network
	peer, ok := n.find(r.PathValue("device"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	var draft botDraft
	if err := readPeerJSON(w, r, &draft); err != nil {
		writeJSON(w, nil, err)
		return
	}
	raw, _ := json.Marshal(draft)
	resp, err := n.request(r.Context(), peer, "POST", "/bots", bytes.NewReader(raw), "")
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	defer resp.Body.Close()
	var p BotProfile
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPeerBody)).Decode(&p); err != nil ||
		!botIDPattern.MatchString(p.ID) || !validDeviceName(p.Name) || p.AvatarSlot < 0 || p.AvatarSlot >= avatarSlots {
		writeJSON(w, nil, errors.New("对端设备没有返回新 Bot，请刷新设备列表"))
		return
	}
	p.Instructions = ""
	writeJSON(w, p, nil)
}

func readPeerJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		return errors.New("请求必须使用 JSON")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPeerBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.Decode(new(any)) != io.EOF {
		return errors.New("无效的消息格式")
	}
	return nil
}

var peerRouteID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

// The same list is enforced on both listeners. In particular there is no
// catch-all forwarding for approvals, secrets or device administration.
func peerBotRoute(method, rest string) bool {
	if method == http.MethodGet {
		switch rest {
		case "info", "stream", "files", "schedules", "settings", "settings/connections":
			return true
		}
	}
	if method != http.MethodPost {
		return false
	}
	switch rest {
	case "messages", "stop", "regenerate", "uploads", "settings", "settings/routes", "settings/connections", "settings/models", "settings/test":
		return true
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || !peerRouteID.MatchString(parts[1]) {
		return false
	}
	if len(parts) == 2 {
		return parts[0] == "cards"
	}
	if len(parts) != 3 {
		return false
	}
	switch parts[0] {
	case "messages":
		return parts[2] == "edit" || parts[2] == "react"
	case "tasks":
		switch parts[2] {
		case "pause", "resume", "cancel", "retry", "top", "up":
			return true
		}
	case "schedules":
		switch parts[2] {
		case "pause", "resume", "run", "delete":
			return true
		}
	}
	return false
}

func limitPeerBody(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method == http.MethodPost {
		limit := int64(1 << 20)
		if rest == "uploads" {
			limit += maxUpload
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
}

func (bs *Bots) proxyPeerBot(w http.ResponseWriter, r *http.Request) {
	n := bs.network
	peer, ok := n.find(r.PathValue("device"))
	id, rest := r.PathValue("bot"), r.PathValue("rest")
	if !ok || !validBotID(id) || !peerBotRoute(r.Method, rest) {
		http.NotFound(w, r)
		return
	}
	var body io.Reader
	if r.Method == http.MethodPost {
		limitPeerBody(w, r, rest)
		body = r.Body
	}
	headers := make(http.Header)
	for _, key := range []string{"Content-Type", "Last-Event-ID", "Range", "If-Range", "If-Modified-Since"} {
		if val := r.Header.Get(key); val != "" {
			headers.Set(key, val)
		}
	}
	target := "/bots/" + id + "/" + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	resp, err := n.do(r.Context(), peer, r.Method, target, body, headers)
	if err != nil {
		peerProxyError(w, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
		peerProxyError(w, "目标设备没有接受请求")
		return
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch rest {
	case "stream":
		if resp.StatusCode != http.StatusOK || media != "text/event-stream" {
			peerProxyError(w, "设备没有返回聊天流")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		rc := http.NewResponseController(w)
		if rc.Flush() == nil {
			_, _ = io.Copy(flushWriter{w, rc}, resp.Body)
		}
	case "files":
		proxyPeerFile(w, resp, media)
	default:
		proxyPeerJSON(w, resp)
	}
}

func proxyPeerJSON(w http.ResponseWriter, resp *http.Response) {
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "application/json" || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode >= 300 && resp.StatusCode < 400 {
		peerProxyError(w, "目标设备没有返回所需数据，请检查设备版本")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func peerProxyError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func proxyPeerFile(w http.ResponseWriter, resp *http.Response, media string) {
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "no-store")
	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 4096))
		return
	}
	disp, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	switch media {
	case "text/plain", "text/markdown", "application/pdf", "application/json",
		"image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "image/x-icon":
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
		disp = "attachment"
	}
	if disp != "attachment" {
		disp = "inline"
	}
	filename := path.Base(strings.ReplaceAll(params["filename"], "\\", "/"))
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": filename}))
	for _, key := range []string{"Content-Length", "Accept-Ranges", "Content-Range", "Last-Modified"} {
		if val := resp.Header.Get(key); val != "" {
			w.Header().Set(key, val)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

type flushWriter struct {
	io.Writer
	rc *http.ResponseController
}

func (w flushWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil {
		err = w.rc.Flush()
	}
	return n, err
}

// Both views consume the same events. Approval arguments and private
// credential descriptions remain on the target device.
func peerChatEvent(e Event) (Event, bool) {
	replace := func(v any) (Event, bool) {
		e.Data, _ = json.Marshal(v)
		return e, true
	}
	switch e.Kind {
	case "message":
		var m Message
		if json.Unmarshal(e.Data, &m) != nil || m.Role == "notice" {
			return e, false
		}
		if m.Secret != nil {
			m.Secret.Description = ""
			return replace(m)
		}
	case "task.created":
		var t TaskRec
		if json.Unmarshal(e.Data, &t) == nil {
			t.History, t.PeerRequest = nil, ""
			return replace(t)
		}
	case "secret.card":
		var s SecretReq
		if json.Unmarshal(e.Data, &s) == nil {
			s.Description = ""
			return replace(s)
		}
	case "agent":
		var ev struct {
			TaskID string            `json:"task_id"`
			Type   onefrog.EventType `json:"type"`
			Time   json.RawMessage   `json:"time"`
			Data   json.RawMessage   `json:"data"`
		}
		if json.Unmarshal(e.Data, &ev) != nil {
			return e, false
		}
		if ev.Type == onefrog.EvTaskStarted {
			ev.Data = json.RawMessage("{}")
			return replace(ev)
		}
		if ev.Type == onefrog.EvApprovalRequested {
			var data struct {
				ID       string `json:"id"`
				ToolName string `json:"tool_name"`
				Risk     string `json:"risk"`
			}
			if json.Unmarshal(ev.Data, &data) != nil {
				return e, false
			}
			ev.Data, _ = json.Marshal(data)
			return replace(ev)
		}
	}
	return e, true
}
