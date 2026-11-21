package remote

import (
	"context"
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
	"unicode/utf8"
)

const (
	ModeOff     = "off"
	ModeReality = "reality" // sing-box VLESS-Reality 隐身直连（云服务器 / 公网 IPv4 / IPv6）
	ModeP2P     = "p2p"     // WireGuard 式 UDP P2P 打洞穿透（家庭内网无公网端口）

	pairTTL = 10 * time.Minute
)

type devIDCtxKey struct{}

type PairedDevice struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	UUID       string    `json:"uuid"`
	PeerID     string    `json:"peer_id"`
	PeerPSK    string    `json:"peer_psk"`
	Activated  bool      `json:"activated"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
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
	Mode           string         `json:"mode"`
	RealityListen  string         `json:"reality_listen"`
	P2PListen      string         `json:"p2p_listen"`
	PublicHost     string         `json:"public_host"`
	SNI            string         `json:"sni"`
	CamouflageDest string         `json:"camouflage_dest,omitempty"`
	ServerName     string         `json:"server_name"`
	RealityPrivKey string         `json:"reality_priv_key"`
	RealityPubKey  string         `json:"reality_pub_key"`
	RealityShortID string         `json:"reality_short_id"`
	P2PPrivKey     string         `json:"p2p_priv_key"`
	P2PPubKey      string         `json:"p2p_pub_key"`
	Devices        []PairedDevice `json:"devices"`

	// 兼容旧版配置文件平滑升级
	LegacyDirectListen string `json:"direct_listen,omitempty"`
}

type SaveInput struct {
	Mode          string `json:"mode"`
	RealityListen string `json:"reality_listen"`
	P2PListen     string `json:"p2p_listen"`
	PublicHost    string `json:"public_host"`
	SNI           string `json:"sni"`
	ServerName    string `json:"server_name"`
}

type StatusView struct {
	Mode          string         `json:"mode"`
	Ready         bool           `json:"ready"`
	StatusText    string         `json:"status_text"`
	Error         string         `json:"error,omitempty"`
	RealityListen string         `json:"reality_listen"`
	P2PListen     string         `json:"p2p_listen"`
	PublicHost    string         `json:"public_host"`
	SNI           string         `json:"sni"`
	ServerName    string         `json:"server_name"`
	DetectedIP    string         `json:"detected_ip"`
	Candidates    []string       `json:"candidates,omitempty"`
	Devices       []PublicDevice `json:"devices"`
}

type PairResponse struct {
	PairCode  string    `json:"pair_code"`
	QRSVG     string    `json:"qr_svg"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Manager struct {
	dataDir string
	app     http.Handler

	internalLn   *chanListener
	internalHTTP *http.Server

	mu            sync.RWMutex
	cfg           Config
	lastTouchSave time.Time

	realityInb *realityInbound
	p2pInb     *p2pInbound
	runtimeErr string
}

func NewManager(dataDir string, app http.Handler) (*Manager, error) {
	m := &Manager{
		dataDir:    dataDir,
		app:        app,
		internalLn: newChanListener(),
	}
	m.internalHTTP = &http.Server{
		Handler:           m.tunnelHTTPHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       95 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if tc, ok := c.(*taggedConn); ok {
				return context.WithValue(ctx, devIDCtxKey{}, tc.devID)
			}
			return ctx
		},
	}
	go func() { _ = m.internalHTTP.Serve(m.internalLn) }()

	m.loadConfig()
	if err := m.applyRuntimeLocked(); err != nil {
		m.runtimeErr = err.Error()
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
	switch cfg.Mode {
	case ModeOff, ModeReality, ModeP2P:
	case "direct":
		cfg.Mode = ModeReality
	default:
		cfg.Mode = ModeOff
	}
	if cfg.RealityListen == "" {
		if cfg.LegacyDirectListen != "" {
			cfg.RealityListen = cfg.LegacyDirectListen
		} else {
			cfg.RealityListen = ":8443"
		}
	}
	cfg.LegacyDirectListen = ""
	if cfg.P2PListen == "" {
		cfg.P2PListen = ":51820"
	}
	if cfg.SNI == "" {
		cfg.SNI = DefaultRealitySNI
	}
	if cfg.ServerName == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			cfg.ServerName = h
		} else {
			cfg.ServerName = "frog"
		}
	}
	ensureKeys(&cfg)

	// 过滤掉无加密凭证的旧版残留设备记录
	validDevs := cfg.Devices[:0]
	now := time.Now()
	for _, d := range cfg.Devices {
		if d.UUID == "" || len(d.PeerID) != 16 || len(d.PeerPSK) != 64 {
			continue
		}
		if !d.Activated && !d.ExpiresAt.IsZero() && now.After(d.ExpiresAt) {
			continue
		}
		validDevs = append(validDevs, d)
	}
	cfg.Devices = validDevs

	m.cfg = cfg
	_ = m.saveConfigLocked()
}

func ensureKeys(cfg *Config) {
	if pub, err := RealityPublicKeyFromPrivate(cfg.RealityPrivKey); err == nil {
		cfg.RealityPubKey = pub
	} else {
		priv, pub, err := GenerateRealityKeyPair()
		if err == nil {
			cfg.RealityPrivKey, cfg.RealityPubKey = priv, pub
		}
	}
	if len(cfg.RealityShortID) != 16 {
		cfg.RealityShortID = randomHex(8)
	} else if _, err := hex.DecodeString(cfg.RealityShortID); err != nil {
		cfg.RealityShortID = randomHex(8)
	}
	if pub, err := RealityPublicKeyFromPrivate(cfg.P2PPrivKey); err == nil {
		cfg.P2PPubKey = pub
	} else {
		priv, pub, err := GenerateRealityKeyPair()
		if err == nil {
			cfg.P2PPrivKey, cfg.P2PPubKey = priv, pub
		}
	}
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
	m.stopRuntimeLocked()
	m.mu.Unlock()
	_ = m.internalLn.Close()
	_ = m.internalHTTP.Close()
}

func (m *Manager) stopRuntimeLocked() {
	if m.realityInb != nil {
		_ = m.realityInb.Close()
		m.realityInb = nil
	}
	if m.p2pInb != nil {
		_ = m.p2pInb.Close()
		m.p2pInb = nil
	}
	m.runtimeErr = ""
}

func (m *Manager) applyRuntimeLocked() error {
	m.stopRuntimeLocked()
	switch m.cfg.Mode {
	case ModeOff:
		return nil

	case ModeReality:
		inb, err := startRealityInbound(realityServerParams{
			Listen:         m.cfg.RealityListen,
			SNI:            m.cfg.SNI,
			CamouflageDest: m.cfg.CamouflageDest,
			PrivateKeyB64:  m.cfg.RealityPrivKey,
			ShortIDHex:     m.cfg.RealityShortID,
			Router:         m.routeTunnelConn,
		})
		if err != nil {
			return err
		}
		m.realityInb = inb
		m.syncPeersLocked()
		return nil

	case ModeP2P:
		inb, err := startP2PInbound(m.cfg.P2PListen, m.cfg.P2PPrivKey, m.routeTunnelConn)
		if err != nil {
			return err
		}
		m.p2pInb = inb
		// 若配置使用了随机端口 :0，固化实际端口以便重启后已配对设备仍能直连
		if strings.HasSuffix(strings.TrimSpace(m.cfg.P2PListen), ":0") && inb.Port() > 0 {
			m.cfg.P2PListen = fmt.Sprintf(":%d", inb.Port())
		}
		m.syncPeersLocked()
		return nil

	default:
		return fmt.Errorf("未知的隧道模式: %s", m.cfg.Mode)
	}
}

func (m *Manager) syncPeersLocked() {
	now := time.Now()
	var ids, uuids []string
	var p2pCreds []P2PPeerCredential

	pruned := m.cfg.Devices[:0]
	for _, d := range m.cfg.Devices {
		if !d.Activated && !d.ExpiresAt.IsZero() && now.After(d.ExpiresAt) {
			continue
		}
		pruned = append(pruned, d)
		ids = append(ids, d.ID)
		uuids = append(uuids, d.UUID)

		var pid [8]byte
		var psk [32]byte
		if _, err1 := hex.Decode(pid[:], []byte(d.PeerID)); err1 == nil {
			if _, err2 := hex.Decode(psk[:], []byte(d.PeerPSK)); err2 == nil {
				p2pCreds = append(p2pCreds, P2PPeerCredential{
					DevID:   d.ID,
					PeerID:  pid,
					PeerPSK: psk,
				})
			}
		}
	}
	m.cfg.Devices = pruned

	if m.realityInb != nil {
		m.realityInb.UpdateUsers(ids, uuids)
	}
	if m.p2pInb != nil {
		m.p2pInb.UpdatePeers(p2pCreds)
	}
}

func (m *Manager) UpdateConfig(in SaveInput) (StatusView, error) {
	mode := strings.TrimSpace(in.Mode)
	switch mode {
	case ModeOff, ModeReality, ModeP2P:
	default:
		return StatusView{}, errors.New("不支持的隧道模式")
	}

	m.mu.Lock()
	prev := m.cfg
	m.cfg.Mode = mode
	if v := strings.TrimSpace(in.RealityListen); v != "" {
		m.cfg.RealityListen = normalizeListenAddr(v, ":8443")
	}
	if v := strings.TrimSpace(in.P2PListen); v != "" {
		m.cfg.P2PListen = normalizeListenAddr(v, ":51820")
	}
	m.cfg.PublicHost = cleanHostInput(in.PublicHost)
	if sni := cleanHostInput(in.SNI); sni != "" {
		m.cfg.SNI = sni
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

// SetCamouflageDest 仅供离线单元测试覆盖 REALITY 伪装回落目标。
func (m *Manager) SetCamouflageDest(dest string) {
	m.mu.Lock()
	m.cfg.CamouflageDest = dest
	if m.cfg.Mode == ModeReality {
		_ = m.applyRuntimeLocked()
	}
	m.mu.Unlock()
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
		if !d.Activated {
			continue
		}
		name := d.Name
		if name == "" {
			name = "移动设备"
		}
		devs = append(devs, PublicDevice{
			ID:         d.ID,
			Name:       name,
			CreatedAt:  d.CreatedAt,
			LastSeenAt: d.LastSeenAt,
		})
	}
	view := StatusView{
		Mode:          m.cfg.Mode,
		RealityListen: m.cfg.RealityListen,
		P2PListen:     m.cfg.P2PListen,
		PublicHost:    m.cfg.PublicHost,
		SNI:           m.cfg.SNI,
		ServerName:    m.cfg.ServerName,
		DetectedIP:    detected,
		Devices:       devs,
	}

	switch m.cfg.Mode {
	case ModeOff:
		view.StatusText = "已关闭"

	case ModeReality:
		if m.runtimeErr != "" {
			view.Error = m.runtimeErr
			view.StatusText = "启动失败"
			break
		}
		if m.realityInb != nil {
			host := m.cfg.PublicHost
			if host == "" {
				host = detected
			}
			view.Ready = true
			view.StatusText = fmt.Sprintf("VLESS-Reality · %s", net.JoinHostPort(host, m.realityInb.Port()))
		}

	case ModeP2P:
		if m.runtimeErr != "" {
			view.Error = m.runtimeErr
			view.StatusText = "启动失败"
			break
		}
		if m.p2pInb != nil {
			cands := m.p2pInb.CandidateEndpoints(m.cfg.PublicHost)
			view.Ready = true
			view.Candidates = cands
			if len(cands) > 0 {
				view.StatusText = fmt.Sprintf("P2P UDP · %s", cands[0])
			} else {
				view.StatusText = fmt.Sprintf("P2P UDP · :%d", m.p2pInb.Port())
			}
		}
	}
	return view
}

// CreatePair 为新客户端签发独立密钥凭证（VLESS UUID 或 P2P PeerKey），生成标准连接码与二维码。
// 未在 10 分钟内完成首次握手的凭证自动作废；一旦连接即永久绑定该设备，支持按台随时吊销。
func (m *Manager) CreatePair() (PairResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg.Mode == ModeOff {
		return PairResponse{}, errors.New("请先开启加密隧道")
	}

	now := time.Now()
	exp := now.Add(pairTTL)
	dev := PairedDevice{
		ID:         "dev_" + randomHex(6),
		Name:       "移动设备",
		UUID:       newUUID(),
		PeerID:     randomHex(8),
		PeerPSK:    randomHex(32),
		Activated:  false,
		ExpiresAt:  exp,
		CreatedAt:  now,
		LastSeenAt: now,
	}

	var uri string
	switch m.cfg.Mode {
	case ModeReality:
		if m.realityInb == nil {
			return PairResponse{}, errors.New("VLESS-Reality 尚未就绪")
		}
		host := m.cfg.PublicHost
		if host == "" {
			host = detectAdvertiseIP()
		}
		uri = BuildRealityURI(RealityClientOptions{
			ServerAddr: net.JoinHostPort(host, m.realityInb.Port()),
			UUID:       dev.UUID,
			SNI:        m.cfg.SNI,
			PublicKey:  m.cfg.RealityPubKey,
			ShortID:    m.cfg.RealityShortID,
			ServerName: m.cfg.ServerName,
		})

	case ModeP2P:
		if m.p2pInb == nil {
			return PairResponse{}, errors.New("P2P 隧道尚未就绪")
		}
		cands := m.p2pInb.CandidateEndpoints(m.cfg.PublicHost)
		uri = BuildP2PURI(P2PClientOptions{
			PeerIDHex:    dev.PeerID,
			PeerPSKHex:   dev.PeerPSK,
			ServerPubB64: m.cfg.P2PPubKey,
			Candidates:   cands,
			ServerName:   m.cfg.ServerName,
		})

	default:
		return PairResponse{}, errors.New("不支持的隧道模式")
	}

	svg, err := EncodeQRSVG(uri)
	if err != nil {
		return PairResponse{}, err
	}

	m.cfg.Devices = append(m.cfg.Devices, dev)
	m.syncPeersLocked()
	_ = m.saveConfigLocked()

	return PairResponse{
		PairCode:  uri,
		QRSVG:     svg,
		ExpiresAt: exp,
	}, nil
}

// RotateGatewaySecret 重新生成服务端密钥对并可清空全部授权设备。
func (m *Manager) RotateGatewaySecret(clearDevices bool) StatusView {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.RealityPrivKey = ""
	m.cfg.RealityPubKey = ""
	m.cfg.RealityShortID = ""
	m.cfg.P2PPrivKey = ""
	m.cfg.P2PPubKey = ""
	ensureKeys(&m.cfg)
	if clearDevices {
		m.cfg.Devices = nil
	}
	_ = m.applyRuntimeLocked()
	_ = m.saveConfigLocked()
	return m.statusLocked()
}

func (m *Manager) RevokeDevice(id string) StatusView {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Devices = slices.DeleteFunc(m.cfg.Devices, func(d PairedDevice) bool { return d.ID == id })
	m.syncPeersLocked()
	_ = m.saveConfigLocked()
	return m.statusLocked()
}

// ---------- 服务端加密隧道流量路由（直达本机 frog + 内网设备） ----------

func (m *Manager) touchAndVerifyDevice(devID, reportedName string) (string, bool) {
	now := time.Now()
	cleanName := strings.TrimSpace(reportedName)
	if utf8.RuneCountInString(cleanName) > 48 {
		cleanName = string([]rune(cleanName)[:48])
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Devices {
		d := &m.cfg.Devices[i]
		if d.ID != devID {
			continue
		}
		if !d.Activated {
			if !d.ExpiresAt.IsZero() && now.After(d.ExpiresAt) {
				m.syncPeersLocked()
				return "", false
			}
			d.Activated = true
			d.ExpiresAt = time.Time{}
			if cleanName != "" {
				d.Name = cleanName
			}
			d.LastSeenAt = now
			m.lastTouchSave = now
			_ = m.saveConfigLocked()
			return d.Name, true
		}
		changed := false
		if cleanName != "" && cleanName != d.Name {
			d.Name = cleanName
			changed = true
		}
		d.LastSeenAt = now
		if changed || now.Sub(m.lastTouchSave) > time.Minute {
			m.lastTouchSave = now
			_ = m.saveConfigLocked()
		}
		return d.Name, true
	}
	return "", false
}

func (m *Manager) routeTunnelConn(ctx context.Context, devID string, conn net.Conn, dest TargetAddr) {
	if _, ok := m.touchAndVerifyDevice(devID, ""); !ok {
		return
	}
	target := dest.String()
	host := dest.Host

	if isFrogInternalHost(host) {
		tc := &taggedConn{Conn: conn, devID: devID, closed: make(chan struct{})}
		if !m.internalLn.Push(tc) {
			return
		}
		select {
		case <-tc.closed:
		case <-ctx.Done():
			_ = tc.Close()
		}
		return
	}

	// 注意：上层 VLESS NewConnectionEx 在函数返回时会关闭 conn；但对于 internalLn，我们需要接管或桥接！
	if !isInternalTarget(host) {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	lanConn, err := (&net.Dialer{}).DialContext(dctx, "tcp", target)
	cancel()
	if err != nil {
		return
	}
	pipeBidirectional(conn, lanConn)
}

func (m *Manager) tunnelHTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		devID, _ := r.Context().Value(devIDCtxKey{}).(string)
		devName, ok := m.touchAndVerifyDevice(devID, r.Header.Get("X-Frog-Device-Name"))
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "设备授权已吊销"})
			return
		}

		// 防跨站请求伪造（供外部 sing-box 浏览器访问内网面板时防护；原生 App 无 Origin 头直接放行）
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "拒绝跨站请求"})
					return
				}
			}
		}

		if r.Method == http.MethodGet && r.URL.Path == "/api/auth/me" {
			writeJSON(w, http.StatusOK, map[string]any{
				"enabled":       false,
				"authenticated": true,
				"username":      devName,
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/auth/logout" {
			m.RevokeDevice(devID)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}

		m.app.ServeHTTP(w, r)
	})
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
