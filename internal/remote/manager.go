package remote

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	ModeOff    = "off"
	ModeQuick  = "quick"  // 免服务器一键免费云穿透（Cloudflare Quick Tunnel）
	ModeBridge = "bridge" // 自建加密中转穿透（frogbot bridge）
	ModeDirect = "direct" // 直连加密模式（内置自签 TLS 1.3 + 指纹钉扎）

	pairTTL = 10 * time.Minute
)

type PairedDevice struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	TokenHash  string    `json:"token_hash"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type PublicDevice struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type Config struct {
	Mode          string         `json:"mode"`
	DirectListen  string         `json:"direct_listen"`
	PublicHost    string         `json:"public_host"`
	BridgeURI     string         `json:"bridge_uri"`
	ServerName    string         `json:"server_name"`
	TunnelID      string         `json:"tunnel_id"`
	GatewaySecret string         `json:"gateway_secret"`
	Devices       []PairedDevice `json:"devices"`
}

type SaveInput struct {
	Mode         string `json:"mode"`
	DirectListen string `json:"direct_listen"`
	PublicHost   string `json:"public_host"`
	BridgeURI    string `json:"bridge_uri"`
	ServerName   string `json:"server_name"`
}

type StatusView struct {
	Mode          string         `json:"mode"`
	Ready         bool           `json:"ready"`
	StatusText    string         `json:"status_text"`
	Error         string         `json:"error,omitempty"`
	EndpointURL   string         `json:"endpoint_url,omitempty"`
	TLSPin        string         `json:"tls_pin,omitempty"`
	DirectListen  string         `json:"direct_listen"`
	PublicHost    string         `json:"public_host"`
	BridgeURI     string         `json:"bridge_uri"`
	ServerName    string         `json:"server_name"`
	DetectedIP    string         `json:"detected_ip"`
	BridgeCommand string         `json:"bridge_command"`
	Devices       []PublicDevice `json:"devices"`
}

type PairResponse struct {
	PairCode  string    `json:"pair_code"`
	QRSVG     string    `json:"qr_svg"`
	ExpiresAt time.Time `json:"expires_at"`
}

type pendingPair struct {
	expires time.Time
}

type failBucket struct {
	count int
	until time.Time
}

type Manager struct {
	dataDir string
	app     http.Handler

	mu          sync.RWMutex
	cfg         Config
	pairs       map[string]pendingPair
	fails       map[string]failBucket
	lastTouchSave time.Time

	// 当前激活的运行时
	directLn  net.Listener
	directSv  *http.Server
	directPin string
	directPort string
	directErr string

	bridgeCli *bridgeTunnelClient
	bridgeParsed parsedBridgeURI

	quickTun *quickTunnel
}

func NewManager(dataDir string, app http.Handler) (*Manager, error) {
	m := &Manager{
		dataDir: dataDir,
		app:     app,
		pairs:   map[string]pendingPair{},
		fails:   map[string]failBucket{},
	}
	m.loadConfig()
	if err := m.applyRuntimeLocked(); err != nil {
		// 启动时不因远程端口被占用而阻止主进程，记录在状态中供界面展示
		m.directErr = err.Error()
	}
	return m, nil
}

func (m *Manager) cfgPath() string {
	return filepath.Join(m.dataDir, "remote.json")
}

func (m *Manager) loadConfig() {
	var cfg Config
	if raw, err := os.ReadFile(m.cfgPath()); err == nil {
		_ = json.Unmarshal(raw, &cfg)
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeOff
	}
	if cfg.DirectListen == "" {
		cfg.DirectListen = ":8443"
	}
	if cfg.ServerName == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			cfg.ServerName = h
		} else {
			cfg.ServerName = "我的电脑"
		}
	}
	if len(cfg.TunnelID) < 24 {
		cfg.TunnelID = randomHex(12)
	}
	if len(cfg.GatewaySecret) < 24 {
		cfg.GatewaySecret = randomHex(12)
	}
	m.cfg = cfg
	_ = m.saveConfigLocked()
}

func (m *Manager) saveConfigLocked() error {
	if err := os.MkdirAll(m.dataDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.dataDir, "remote-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	err = enc.Encode(m.cfg)
	closeErr := tmp.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	_ = os.Chmod(tmp.Name(), 0o600)
	return os.Rename(tmp.Name(), m.cfgPath())
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopRuntimeLocked()
}

func (m *Manager) stopRuntimeLocked() {
	if m.directLn != nil {
		_ = m.directLn.Close()
		m.directLn = nil
	}
	if m.directSv != nil {
		_ = m.directSv.Close()
		m.directSv = nil
	}
	m.directPin = ""
	m.directPort = ""
	m.directErr = ""

	if m.bridgeCli != nil {
		b := m.bridgeCli
		m.bridgeCli = nil
		go b.Close()
	}
	if m.quickTun != nil {
		q := m.quickTun
		m.quickTun = nil
		go q.Close()
	}
}

func (m *Manager) applyRuntimeLocked() error {
	m.stopRuntimeLocked()
	gw := m.gatewayHandler()

	switch m.cfg.Mode {
	case ModeOff:
		return nil

	case ModeDirect:
		cert, fp, err := EnsureCert(
			filepath.Join(m.dataDir, "remote_tls.crt"),
			filepath.Join(m.dataDir, "remote_tls.key"),
		)
		if err != nil {
			return err
		}
		listen := strings.TrimSpace(m.cfg.DirectListen)
		if listen == "" {
			listen = ":8443"
		}
		if !strings.Contains(listen, ":") {
			listen = ":" + listen
		}
		ln, err := tls.Listen("tcp", listen, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		})
		if err != nil {
			return fmt.Errorf("监听 %s 失败: %w", listen, err)
		}
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		sv := &http.Server{
			Handler:           gw,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       95 * time.Second,
		}
		m.directLn = ln
		m.directSv = sv
		m.directPin = fp
		m.directPort = port
		go func() { _ = sv.Serve(ln) }()
		return nil

	case ModeBridge:
		cli, parsed, err := startBridgeTunnelClient(m.cfg.BridgeURI, m.cfg.TunnelID, gw)
		if err != nil {
			return err
		}
		m.bridgeCli = cli
		m.bridgeParsed = parsed
		return nil

	case ModeQuick:
		qt, err := startQuickTunnel(m.dataDir, gw)
		if err != nil {
			return err
		}
		m.quickTun = qt
		return nil

	default:
		return fmt.Errorf("未知的远程模式: %s", m.cfg.Mode)
	}
}

func (m *Manager) UpdateConfig(in SaveInput) (StatusView, error) {
	mode := strings.TrimSpace(in.Mode)
	switch mode {
	case ModeOff, ModeQuick, ModeBridge, ModeDirect:
	default:
		return StatusView{}, errors.New("不支持的远程模式")
	}
	if mode == ModeBridge {
		if _, err := parseBridgeURI(in.BridgeURI); err != nil {
			return StatusView{}, err
		}
	}

	m.mu.Lock()
	prev := m.cfg
	m.cfg.Mode = mode
	if strings.TrimSpace(in.DirectListen) != "" {
		m.cfg.DirectListen = strings.TrimSpace(in.DirectListen)
	}
	m.cfg.PublicHost = strings.TrimSpace(in.PublicHost)
	if strings.TrimSpace(in.BridgeURI) != "" || mode != ModeBridge {
		if strings.TrimSpace(in.BridgeURI) != "" {
			m.cfg.BridgeURI = strings.TrimSpace(in.BridgeURI)
		}
	}
	if name := strings.TrimSpace(in.ServerName); name != "" {
		m.cfg.ServerName = name
	}
	if err := m.applyRuntimeLocked(); err != nil {
		m.cfg = prev
		_ = m.applyRuntimeLocked()
		m.mu.Unlock()
		return StatusView{}, err
	}
	_ = m.saveConfigLocked()
	st := m.statusLocked()
	m.mu.Unlock()
	return st, nil
}

func (m *Manager) Status() StatusView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.statusLocked()
}

func (m *Manager) statusLocked() StatusView {
	detected := detectAdvertiseIP()
	devs := make([]PublicDevice, 0, len(m.cfg.Devices))
	for _, d := range m.cfg.Devices {
		devs = append(devs, PublicDevice{
			ID:         d.ID,
			Name:       d.Name,
			CreatedAt:  d.CreatedAt,
			LastSeenAt: d.LastSeenAt,
		})
	}
	view := StatusView{
		Mode:          m.cfg.Mode,
		DirectListen:  m.cfg.DirectListen,
		PublicHost:    m.cfg.PublicHost,
		BridgeURI:     m.cfg.BridgeURI,
		ServerName:    m.cfg.ServerName,
		DetectedIP:    detected,
		BridgeCommand: "frogbot bridge -listen :8443",
		Devices:       devs,
	}

	switch m.cfg.Mode {
	case ModeOff:
		view.StatusText = "未开启远程访问"

	case ModeDirect:
		if m.directErr != "" {
			view.Error = m.directErr
			view.StatusText = "启动失败"
			break
		}
		if m.directLn != nil {
			host := m.cfg.PublicHost
			if host == "" {
				host = detected
			}
			// 若用户填了带端口或纯主机，统一规整
			if strings.Contains(host, "://") {
				if u, err := url.Parse(host); err == nil && u.Hostname() != "" {
					host = u.Hostname()
				}
			}
			if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
				host = h
			}
			view.Ready = true
			view.TLSPin = m.directPin
			view.EndpointURL = fmt.Sprintf("https://%s/r/%s", net.JoinHostPort(host, m.directPort), m.cfg.GatewaySecret)
			view.StatusText = "直连加密监听中（TLS 1.3 + 证书指纹锁定）"
		}

	case ModeBridge:
		if m.bridgeCli != nil {
			ok, lastErr := m.bridgeCli.Status()
			view.TLSPin = m.bridgeParsed.PinHex
			view.EndpointURL = fmt.Sprintf("https://%s/t/%s/r/%s", m.bridgeParsed.HostPort, m.cfg.TunnelID, m.cfg.GatewaySecret)
			if ok {
				view.Ready = true
				view.StatusText = "已连通自建加密中转节点（本机零端口暴露）"
			} else {
				view.StatusText = "正在连接中转节点…"
				view.Error = lastErr
			}
		}

	case ModeQuick:
		if m.quickTun != nil {
			base, stText, lastErr := m.quickTun.State()
			view.StatusText = stText
			view.Error = lastErr
			if base != "" {
				view.Ready = true
				view.EndpointURL = strings.TrimRight(base, "/") + "/r/" + m.cfg.GatewaySecret
			}
		}
	}
	return view
}

// CreatePair 生成 10 分钟有效的一次性配对码与矢量二维码。
func (m *Manager) CreatePair() (PairResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.statusLocked()
	if !st.Ready || st.EndpointURL == "" {
		if st.Mode == ModeOff {
			return PairResponse{}, errors.New("请先选择一种远程连接模式并开启")
		}
		return PairResponse{}, errors.New("加密通道尚未就绪，请稍候几秒再试")
	}

	now := time.Now()
	for k, p := range m.pairs {
		if now.After(p.expires) {
			delete(m.pairs, k)
		}
	}
	tok := randomHex(16)
	exp := now.Add(pairTTL)
	m.pairs[tok] = pendingPair{expires: exp}

	q := url.Values{}
	q.Set("u", st.EndpointURL)
	q.Set("t", tok)
	if st.TLSPin != "" {
		q.Set("fp", st.TLSPin)
	}
	if st.ServerName != "" {
		q.Set("n", st.ServerName)
	}
	code := "frogpair://connect?" + q.Encode()
	svg, err := EncodeQRSVG(code)
	if err != nil {
		return PairResponse{}, err
	}
	return PairResponse{
		PairCode:  code,
		QRSVG:     svg,
		ExpiresAt: exp,
	}, nil
}

// RotateGatewaySecret 重置随机安全路径并清空所有已配对设备。
func (m *Manager) RotateGatewaySecret(clearDevices bool) StatusView {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.GatewaySecret = randomHex(12)
	m.pairs = map[string]pendingPair{}
	if clearDevices {
		m.cfg.Devices = nil
	}
	_ = m.saveConfigLocked()
	return m.statusLocked()
}

func (m *Manager) RevokeDevice(id string) StatusView {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Devices = slices.DeleteFunc(m.cfg.Devices, func(d PairedDevice) bool { return d.ID == id })
	_ = m.saveConfigLocked()
	return m.statusLocked()
}

// ---------- 管理端 HTTP 路由（挂在主服务 /api/settings/remote 下） ----------

func (m *Manager) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings/remote", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.Status())
	})
	mux.HandleFunc("POST /api/settings/remote", func(w http.ResponseWriter, r *http.Request) {
		var in SaveInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
			return
		}
		st, err := m.UpdateConfig(in)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /api/settings/remote/pair", func(w http.ResponseWriter, r *http.Request) {
		res, err := m.CreatePair()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /api/settings/remote/rotate", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ClearDevices bool `json:"clear_devices"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in)
		writeJSON(w, http.StatusOK, m.RotateGatewaySecret(in.ClearDevices))
	})
	mux.HandleFunc("DELETE /api/settings/remote/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.RevokeDevice(r.PathValue("id")))
	})
	return mux
}

// ---------- 远程安全网关（隐藏路径 + 一次性配对 + 独立设备令牌鉴权） ----------

func (m *Manager) gatewayHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// 1. 校验秘密入口路径 /r/{gatewaySecret}，不匹配一律静默返回 404，杜绝扫描器识别与爆破
		if !strings.HasPrefix(r.URL.Path, "/r/") {
			http.NotFound(w, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/r/")
		seg := rest
		subPath := "/"
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			seg = rest[:idx]
			subPath = rest[idx:]
		}

		m.mu.RLock()
		wantSecret := m.cfg.GatewaySecret
		srvName := m.cfg.ServerName
		m.mu.RUnlock()

		if len(seg) != len(wantSecret) || subtle.ConstantTimeCompare([]byte(seg), []byte(wantSecret)) != 1 {
			http.NotFound(w, r)
			return
		}

		// 2. 同源检查（防跨站请求伪造；iOS/macOS 原生客户端无 Origin 头自然放行）
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "拒绝跨站请求"})
					return
				}
			}
		}

		// 3. 一次性配对兑换接口
		if r.Method == http.MethodPost && subPath == "/api/remote/pair" {
			m.handlePairRedeem(w, r, srvName)
			return
		}

		// 4. 登录态查询与退出
		devName, devID, ok := m.authenticateDevice(r)
		if r.Method == http.MethodGet && subPath == "/api/auth/me" {
			writeJSON(w, http.StatusOK, map[string]any{
				"enabled":       true,
				"authenticated": ok,
				"username":      devName,
			})
			return
		}
		if r.Method == http.MethodPost && subPath == "/api/auth/logout" {
			if ok && devID != "" {
				m.RevokeDevice(devID)
			}
			http.SetCookie(w, &http.Cookie{
				Name:     "frogbot_session",
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
			})
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}

		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "远程授权已失效，请在客户端重新扫码或粘贴配对码连接"})
			return
		}

		r2 := r.Clone(r.Context())
		r2.URL.Path = subPath
		r2.URL.RawPath = ""
		m.app.ServeHTTP(w, r2)
	})
}

func (m *Manager) handlePairRedeem(w http.ResponseWriter, r *http.Request, srvName string) {
	ip := remoteIP(r)
	now := time.Now()

	m.mu.Lock()
	if fb, exists := m.fails[ip]; exists && now.Before(fb.until) {
		m.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "尝试次数过多，请稍后再试"})
		return
	}
	m.mu.Unlock()

	var body struct {
		Token      string `json:"token"`
		DeviceName string `json:"device_name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的配对请求"})
		return
	}
	tok := strings.TrimSpace(body.Token)
	devName := strings.TrimSpace(body.DeviceName)
	if devName == "" {
		devName = "移动设备"
	}
	if len([]rune(devName)) > 48 {
		devName = string([]rune(devName)[:48])
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.pairs[tok]
	if !ok || now.After(p.expires) {
		delete(m.pairs, tok)
		fb := m.fails[ip]
		fb.count++
		if fb.count >= 6 {
			fb.until = now.Add(5 * time.Minute)
		}
		m.fails[ip] = fb
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "配对码无效或已过期，请在电脑端重新生成"})
		return
	}
	// 一次性立即销毁
	delete(m.pairs, tok)
	delete(m.fails, ip)

	sessionToken := "fgr_" + randomHex(32)
	sum := sha256.Sum256([]byte(sessionToken))
	dev := PairedDevice{
		ID:         "dev_" + randomHex(6),
		Name:       devName,
		TokenHash:  hex.EncodeToString(sum[:]),
		CreatedAt:  now,
		LastSeenAt: now,
	}
	m.cfg.Devices = append(m.cfg.Devices, dev)
	_ = m.saveConfigLocked()

	http.SetCookie(w, &http.Cookie{
		Name:     "frogbot_session",
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"session":     sessionToken,
		"server_name": srvName,
		"device_id":   dev.ID,
	})
}

func (m *Manager) authenticateDevice(r *http.Request) (name, id string, ok bool) {
	c, err := r.Cookie("frogbot_session")
	if err != nil || len(c.Value) < 32 {
		return "", "", false
	}
	sum := sha256.Sum256([]byte(c.Value))
	want := hex.EncodeToString(sum[:])

	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Devices {
		d := &m.cfg.Devices[i]
		if len(d.TokenHash) == len(want) && subtle.ConstantTimeCompare([]byte(d.TokenHash), []byte(want)) == 1 {
			d.LastSeenAt = now
			if now.Sub(m.lastTouchSave) > time.Minute {
				m.lastTouchSave = now
				_ = m.saveConfigLocked()
			}
			return d.Name, d.ID, true
		}
	}
	return "", "", false
}

func remoteIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
