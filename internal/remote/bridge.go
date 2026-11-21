package remote

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type bridgeSignal struct {
	Op       string `json:"op"`
	StreamID string `json:"sid,omitempty"`
}

// BridgeConfig 保存 frogbot bridge 中转节点的持久化令牌。
type BridgeConfig struct {
	Token string `json:"token"`
}

// BridgeServer 是在公网轻量 VPS 上运行的单端口 TLS 1.3 反向隧道中转服务。
// 内网 frogbot 主动发起长连接注册隧道（无需开放任何公网端口），外部手机通过 /t/{tunnelID}/... 访问。
type BridgeServer struct {
	token string

	mu      sync.RWMutex
	tunnels map[string]*bridgeTunnel
	waiters map[string]chan net.Conn
}

type bridgeTunnel struct {
	id      string
	conn    net.Conn
	writeMu sync.Mutex
	proxy   *httputil.ReverseProxy
	tr      *http.Transport
	closed  chan struct{}
}

func NewBridgeServer(token string) *BridgeServer {
	return &BridgeServer{
		token:   token,
		tunnels: map[string]*bridgeTunnel{},
		waiters: map[string]chan net.Conn{},
	}
}

func (b *BridgeServer) checkToken(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return len(got) >= 32 && subtle.ConstantTimeCompare([]byte(got), []byte(b.token)) == 1
}

func (b *BridgeServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_frog_bridge/control", b.handleControl)
	mux.HandleFunc("GET /_frog_bridge/stream", b.handleStream)
	mux.HandleFunc("/t/", b.handleProxy)
	mux.HandleFunc("/", http.NotFound)
	return mux
}

func (b *BridgeServer) handleControl(w http.ResponseWriter, r *http.Request) {
	if !b.checkToken(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if len(id) < 16 || len(id) > 64 {
		http.Error(w, "invalid tunnel id", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: frog-bridge\r\nConnection: Upgrade\r\n\r\n")

	tun := &bridgeTunnel{
		id:     id,
		conn:   conn,
		closed: make(chan struct{}),
	}
	tun.tr = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return b.openStream(ctx, tun)
		},
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
	target, _ := url.Parse("http://frog.tunnel")
	tun.proxy = &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = req.Host
		},
		Transport:     tun.tr,
		FlushInterval: -1, // 立即刷新 SSE 流
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(rw, "内网主机离线或连接中断", http.StatusBadGateway)
		},
	}

	b.mu.Lock()
	if old := b.tunnels[id]; old != nil {
		_ = old.conn.Close()
		old.tr.CloseIdleConnections()
	}
	b.tunnels[id] = tun
	b.mu.Unlock()

	defer func() {
		close(tun.closed)
		_ = conn.Close()
		tun.tr.CloseIdleConnections()
		b.mu.Lock()
		if b.tunnels[id] == tun {
			delete(b.tunnels, id)
		}
		b.mu.Unlock()
	}()

	// 定时发送 ping 保活
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := tun.sendSignal(bridgeSignal{Op: "ping"}); err != nil {
					_ = conn.Close()
					return
				}
			case <-tun.closed:
				return
			}
		}
	}()

	// 读取客户端发来的 pong 或断开
	sc := bufio.NewScanner(buf)
	for sc.Scan() {
		_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	}
}

func (t *bridgeTunnel) sendSignal(sig bridgeSignal) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
	data, _ := json.Marshal(sig)
	data = append(data, '\n')
	_, err := t.conn.Write(data)
	return err
}

func (b *BridgeServer) openStream(ctx context.Context, tun *bridgeTunnel) (net.Conn, error) {
	sid := randomHex(16)
	ch := make(chan net.Conn, 1)
	b.mu.Lock()
	b.waiters[sid] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waiters, sid)
		b.mu.Unlock()
	}()

	if err := tun.sendSignal(bridgeSignal{Op: "open", StreamID: sid}); err != nil {
		return nil, err
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case c := <-ch:
		return c, nil
	case <-tun.closed:
		return nil, errors.New("tunnel closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("timeout waiting for tunnel stream")
	}
}

func (b *BridgeServer) handleStream(w http.ResponseWriter, r *http.Request) {
	if !b.checkToken(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sid := strings.TrimSpace(r.URL.Query().Get("sid"))
	b.mu.RLock()
	ch := b.waiters[sid]
	b.mu.RUnlock()
	if ch == nil {
		http.Error(w, "unknown stream", http.StatusGone)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: frog-stream\r\nConnection: Upgrade\r\n\r\n")
	_ = conn.SetDeadline(time.Time{})
	select {
	case ch <- conn:
	default:
		_ = conn.Close()
	}
}

func (b *BridgeServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	// URL 形如 /t/{tunnelID}/r/{secret}/...
	rest := strings.TrimPrefix(r.URL.Path, "/t/")
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		http.NotFound(w, r)
		return
	}
	tid := rest[:slash]
	subPath := rest[slash:]

	b.mu.RLock()
	tun := b.tunnels[tid]
	b.mu.RUnlock()
	if tun == nil {
		http.Error(w, "该远程主机当前不在线", http.StatusBadGateway)
		return
	}

	r2 := r.Clone(r.Context())
	r2.URL.Path = subPath
	r2.URL.RawPath = ""
	tun.proxy.ServeHTTP(w, r2)
}

// ---------- 内网侧主动连接 Bridge 的反向隧道客户端 ----------

type parsedBridgeURI struct {
	HostPort string
	Token    string
	PinHex   string
}

func parseBridgeURI(raw string) (parsedBridgeURI, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "frog-bridge" && u.Scheme != "https") || u.Host == "" {
		return parsedBridgeURI{}, errors.New("中转连接串格式不对，应形如 frog-bridge://IP:端口#令牌.证书指纹")
	}
	parts := strings.SplitN(u.Fragment, ".", 2)
	if len(parts) != 2 || len(parts[0]) < 32 || len(parts[1]) != 64 {
		return parsedBridgeURI{}, errors.New("中转连接串缺少有效的令牌或证书指纹（请直接复制 frogbot bridge 打印的完整连接串）")
	}
	hostPort := u.Host
	if !strings.Contains(hostPort, ":") {
		hostPort += ":8443"
	}
	return parsedBridgeURI{
		HostPort: hostPort,
		Token:    parts[0],
		PinHex:   strings.ToLower(parts[1]),
	}, nil
}

type chanListener struct {
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn, 32), closed: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

type bridgeTunnelClient struct {
	parsed   parsedBridgeURI
	tunnelID string
	tlsCfg   *tls.Config
	ln       *chanListener
	srv      *http.Server
	cancel   context.CancelFunc
	done     chan struct{}

	mu        sync.RWMutex
	connected bool
	lastErr   string
}

func startBridgeTunnelClient(uri, tunnelID string, gateway http.Handler) (*bridgeTunnelClient, parsedBridgeURI, error) {
	parsed, err := parseBridgeURI(uri)
	if err != nil {
		return nil, parsedBridgeURI{}, err
	}
	// Hijacker 需要 HTTP/1.1 ALPN
	tlsCfg, err := NewPinnedTLSConfig(parsed.PinHex, []string{"http/1.1"})
	if err != nil {
		return nil, parsedBridgeURI{}, err
	}
	ln := newChanListener()
	ctx, cancel := context.WithCancel(context.Background())
	c := &bridgeTunnelClient{
		parsed:   parsed,
		tunnelID: tunnelID,
		tlsCfg:   tlsCfg,
		ln:       ln,
		srv: &http.Server{
			Handler:           gateway,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       95 * time.Second,
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go func() { _ = c.srv.Serve(ln) }()
	go c.supervise(ctx)
	return c, parsed, nil
}

func (c *bridgeTunnelClient) Status() (connected bool, lastErr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected, c.lastErr
}

func (c *bridgeTunnelClient) setStatus(ok bool, errMsg string) {
	c.mu.Lock()
	c.connected = ok
	c.lastErr = errMsg
	c.mu.Unlock()
}

func (c *bridgeTunnelClient) Close() {
	c.cancel()
	_ = c.ln.Close()
	_ = c.srv.Close()
	<-c.done
}

func (c *bridgeTunnelClient) supervise(ctx context.Context) {
	defer close(c.done)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.runControlOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.setStatus(false, err.Error())
		} else {
			c.setStatus(false, "")
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (c *bridgeTunnelClient) dialUpgrade(ctx context.Context, path string) (net.Conn, *bufio.Reader, error) {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second},
		Config:    c.tlsCfg,
	}
	conn, err := d.DialContext(ctx, "tcp", c.parsed.HostPort)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nConnection: Upgrade\r\nUpgrade: frog-bridge\r\n\r\n",
		path, c.parsed.HostPort, c.parsed.Token)
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = conn.Close()
		return nil, nil, fmt.Errorf("中转握手被拒绝 (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, br, nil
}

func (c *bridgeTunnelClient) runControlOnce(ctx context.Context) error {
	conn, br, err := c.dialUpgrade(ctx, "/_frog_bridge/control?id="+url.QueryEscape(c.tunnelID))
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	c.setStatus(true, "")
	sc := bufio.NewScanner(br)
	for sc.Scan() {
		var sig bridgeSignal
		if json.Unmarshal(sc.Bytes(), &sig) != nil {
			continue
		}
		switch sig.Op {
		case "ping":
			_ = conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
			_, _ = io.WriteString(conn, "{\"op\":\"pong\"}\n")
		case "open":
			if sig.StreamID != "" {
				go c.acceptStream(ctx, sig.StreamID)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("连接已断开")
}

func (c *bridgeTunnelClient) acceptStream(ctx context.Context, sid string) {
	conn, br, err := c.dialUpgrade(ctx, "/_frog_bridge/stream?sid="+url.QueryEscape(sid))
	if err != nil {
		return
	}
	var wrapped net.Conn = conn
	if br.Buffered() > 0 {
		wrapped = &bufferedConn{Conn: conn, r: br}
	}
	select {
	case c.ln.ch <- wrapped:
	case <-c.ln.closed:
		_ = conn.Close()
	case <-ctx.Done():
		_ = conn.Close()
	}
}

// RunBridgeCLI 运行公网轻量加密中转节点子命令：frogbot bridge [-listen :8443] [-host 公网IP] [-data 目录]
func RunBridgeCLI(args []string) {
	fs := flag.NewFlagSet("bridge", flag.ExitOnError)
	listenAddr := fs.String("listen", ":8443", "中转服务监听地址（默认 :8443）")
	publicHost := fs.String("host", "", "对外显示的公网 IP 或域名（留空则自动探测）")
	dataDir := fs.String("data", "", "中转证书与令牌保存目录（默认 ~/.frogbot-bridge）")
	_ = fs.Parse(args)

	dir := *dataDir
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".frogbot-bridge")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatalf("创建目录失败: %v", err)
	}

	cfgPath := filepath.Join(dir, "bridge.json")
	var cfg BridgeConfig
	if raw, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(raw, &cfg)
	}
	if len(cfg.Token) < 32 {
		cfg.Token = randomHex(24)
		raw, _ := json.MarshalIndent(cfg, "", "  ")
		_ = os.WriteFile(cfgPath, raw, 0o600)
	}

	cert, fp, err := EnsureCert(filepath.Join(dir, "bridge_tls.crt"), filepath.Join(dir, "bridge_tls.key"))
	if err != nil {
		log.Fatalf("生成中转 TLS 证书失败: %v", err)
	}

	ln, err := tls.Listen("tcp", *listenAddr, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"}, // 兼容 Upgrade/Hijack 与普通 HTTPS 请求
	})
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", *listenAddr, err)
	}

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	host := strings.TrimSpace(*publicHost)
	if host == "" {
		host = detectAdvertiseIP()
	}
	uri := fmt.Sprintf("frog-bridge://%s:%s#%s.%s", host, port, cfg.Token, fp)

	fmt.Printf("\n  🐸 frog 加密中转节点已启动\n\n")
	fmt.Printf("  监听端口： %s (TLS 1.3)\n", ln.Addr())
	fmt.Printf("  数据目录： %s\n\n", dir)
	fmt.Printf("  请把下面这行「中转连接串」粘贴到家里电脑 frog 设置 › 远程访问 中：\n\n")
	fmt.Printf("  %s\n\n", uri)
	if host == "127.0.0.1" || strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "172.") {
		fmt.Printf("  提示：如果这台机器有公网 IP 但网卡是内网地址，请把连接串里的 %s 换成公网 IP（或加 -host <公网IP> 启动）。\n\n", host)
	}

	srv := &http.Server{
		Handler:           NewBridgeServer(cfg.Token).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       95 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("中转服务异常退出: %v", err)
	}
}
