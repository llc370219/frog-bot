package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// UU 远程 / Tailscale DERP 同款三层穿透协议：
//   1. 公网信令注册（TCP/TLS 1.3）：内网主机主动外连中继并保活，无需公网 IP 与端口映射。
//   2. 双向同步 UDP 打洞：中继同时向主机与手机交换双方真实候选端点（含同端口 UDP Echo 探测出的 NAT 映射地址），双方同毫秒对射打洞。
//   3. 端到端加密中继兜底：若 1.5s 内 UDP 未打通（对称 NAT / 5G 限 UDP / 本机 TUN 代理），自动复用已建立的 TLS 1.3 连接转发原始
//      X25519+ChaCha20-Poly1305 密文包，中继节点零明文可见。

const (
	relayMsgHostRegister byte = 0x01
	relayMsgHostOK       byte = 0x02
	relayMsgHostUpdate   byte = 0x03
	relayMsgPong         byte = 0x04
	relayMsgClientJoin   byte = 0x05
	relayMsgPunch        byte = 0x06
	relayMsgHostInfo     byte = 0x07
	relayMsgPacket       byte = 0x08
	relayMsgClientClosed byte = 0x09
	relayMsgPing         byte = 0x0A
	relayMsgError        byte = 0x7F

	relayMaxFrameBytes = 8192
)

var (
	relayUDPEchoReqMagic  = [4]byte{0xF0, 0x52, 0x45, 0x4C} // \xf0REL
	relayUDPEchoRespMagic = [4]byte{0xF1, 0x52, 0x45, 0x4C}
	p2pPunchMagic         = [8]byte{0x00, 'F', 'R', 'O', 'G', 'P', 'N', 'C'}
)

// HostIDFromP2PPubKey 由主机 P2P X25519 公钥派生 32 位十六进制主机 ID（16 字节 SHA-256）。
func HostIDFromP2PPubKey(pubB64 string) string {
	raw, err := decodeBase64Flexible(pubB64)
	if err != nil || len(raw) != 32 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// relayPeerAddr 实现 net.Addr，代表经由中继服务器转发的逻辑对端（用于无缝复用 arqSession）。
type relayPeerAddr struct {
	connID uint32
}

func (a relayPeerAddr) Network() string { return "frog-relay" }
func (a relayPeerAddr) String() string  { return fmt.Sprintf("relay:%d", a.connID) }

// ---------- 二进制帧读写 ----------

func writeRelayFrame(w io.Writer, msgType byte, payload []byte) error {
	if len(payload) > relayMaxFrameBytes {
		return errors.New("relay frame too large")
	}
	buf := make([]byte, 3+len(payload))
	buf[0] = msgType
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(payload)))
	copy(buf[3:], payload)
	_, err := w.Write(buf)
	return err
}

func readRelayFrame(r io.Reader) (byte, []byte, error) {
	var hdr [3]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[1:3]))
	if n > relayMaxFrameBytes {
		return 0, nil, errors.New("relay frame exceeds limit")
	}
	if n == 0 {
		return hdr[0], nil, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

func encodeRelayPacketPayload(connID uint32, pkt []byte) []byte {
	out := make([]byte, 4+len(pkt))
	binary.BigEndian.PutUint32(out[0:4], connID)
	copy(out[4:], pkt)
	return out
}

func decodeRelayPacketPayload(payload []byte) (uint32, []byte, bool) {
	if len(payload) < 5 {
		return 0, nil, false
	}
	return binary.BigEndian.Uint32(payload[0:4]), payload[4:], true
}

func generateSelfSignedRelayTLS() (*tls.Config, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "frog-relay"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  priv,
		}},
	}, nil
}

func dialRelayTLS(ctx context.Context, addr string) (*tls.Conn, error) {
	d := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 20 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	// 内层数据包已由 X25519 + ChaCha20-Poly1305 + PeerPSK 端到端认证加密，外层 TLS 1.3 提供隐蔽传输与抗嗅探。
	tc := tls.Client(raw, &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		ServerName:         "www.apple.com",
	})
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return tc, nil
}

// ---------- 中继服务端（部署在公网 VPS，单端口同时提供 TCP TLS 信令/中继 + UDP Echo NAT 探测） ----------

type RelayServer struct {
	token string

	tcpLn   net.Listener
	udpConn *net.UDPConn
	tcpAddr string
	udpAddr string

	mu    sync.RWMutex
	hosts map[string]*relayHostConn

	nextConnID atomic.Uint32
	ctx        context.Context
	cancel     context.CancelFunc
}

type relayOutFrame struct {
	typ     byte
	payload []byte
}

type relayHostConn struct {
	hostID string
	conn   net.Conn
	sendCh chan relayOutFrame

	mu        sync.RWMutex
	endpoints []string
	clients   map[uint32]*relayClientPeer

	closeOnce sync.Once
	closedCh  chan struct{}
}

type relayClientPeer struct {
	connID uint32
	conn   net.Conn
	sendCh chan relayOutFrame

	closeOnce sync.Once
	closedCh  chan struct{}
}

func StartRelayServer(listen, token string) (*RelayServer, error) {
	listen = normalizeListenAddr(listen, ":18443")
	tlsCfg, err := generateSelfSignedRelayTLS()
	if err != nil {
		return nil, err
	}
	rawLn, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("中继 TCP 监听 %s 失败: %w", listen, err)
	}
	tcpLn := tls.NewListener(rawLn, tlsCfg)

	// UDP 绑定与 TCP 相同的端口号（支持 :0 随机端口测试）
	_, portStr, _ := net.SplitHostPort(rawLn.Addr().String())
	hostPart, _, _ := net.SplitHostPort(listen)
	udpBind := net.JoinHostPort(hostPart, portStr)
	uAddr, err := net.ResolveUDPAddr("udp", udpBind)
	if err != nil {
		_ = tcpLn.Close()
		return nil, err
	}
	udpConn, err := net.ListenUDP("udp", uAddr)
	if err != nil {
		_ = tcpLn.Close()
		return nil, fmt.Errorf("中继 UDP 监听 %s 失败: %w", udpBind, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	rs := &RelayServer{
		token:   strings.TrimSpace(token),
		tcpLn:   tcpLn,
		udpConn: udpConn,
		tcpAddr: rawLn.Addr().String(),
		udpAddr: udpConn.LocalAddr().String(),
		hosts:   map[string]*relayHostConn{},
		ctx:     ctx,
		cancel:  cancel,
	}
	go rs.serveUDP()
	go rs.serveTCP()
	return rs, nil
}

func (s *RelayServer) Addr() string { return s.tcpAddr }

func (s *RelayServer) Close() error {
	s.cancel()
	s.mu.Lock()
	for _, h := range s.hosts {
		h.close()
	}
	s.hosts = map[string]*relayHostConn{}
	s.mu.Unlock()
	_ = s.udpConn.Close()
	return s.tcpLn.Close()
}

func (s *RelayServer) checkToken(got string) bool {
	if s.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(s.token)) == 1
}

// serveUDP 提供极简自建 STUN Echo：回传客户端真实的公网映射 ip:port。
func (s *RelayServer) serveUDP() {
	buf := make([]byte, 512)
	for {
		n, addr, err := s.udpConn.ReadFromUDP(buf)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			continue
		}
		if n < 16 || [4]byte(buf[0:4]) != relayUDPEchoReqMagic {
			continue
		}
		obs := addr.String()
		resp := make([]byte, 16+len(obs))
		copy(resp[0:4], relayUDPEchoRespMagic[:])
		copy(resp[4:16], buf[4:16])
		copy(resp[16:], obs)
		_, _ = s.udpConn.WriteToUDP(resp, addr)
	}
}

func (s *RelayServer) serveTCP() {
	for {
		conn, err := s.tcpLn.Accept()
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			continue
		}
		go s.handleConn(conn)
	}
}

type hostRegPayload struct {
	HostID    string   `json:"host_id"`
	Token     string   `json:"token"`
	Endpoints []string `json:"endpoints"`
}

type clientJoinPayload struct {
	HostID    string   `json:"host_id"`
	Token     string   `json:"token"`
	Endpoints []string `json:"endpoints"`
}

type punchPayload struct {
	ConnID    uint32   `json:"conn_id"`
	Endpoints []string `json:"endpoints"`
}

func (s *RelayServer) sendErr(conn net.Conn, msg string) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_ = writeRelayFrame(conn, relayMsgError, b)
}

func (s *RelayServer) handleConn(conn net.Conn) {
	defer conn.Close()
	if tc, ok := conn.(*tls.Conn); ok {
		ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
		err := tc.HandshakeContext(ctx)
		cancel()
		if err != nil {
			return
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	typ, payload, err := readRelayFrame(conn)
	if err != nil {
		return
	}
	switch typ {
	case relayMsgHostRegister:
		s.handleHost(conn, payload)
	case relayMsgClientJoin:
		s.handleClient(conn, payload)
	default:
		s.sendErr(conn, "无效的握手消息")
	}
}

func cleanEndpointList(in []string) []string {
	var out []string
	for _, ep := range in {
		ep = strings.TrimSpace(ep)
		if ep == "" || len(ep) > 96 {
			continue
		}
		if _, _, err := net.SplitHostPort(ep); err != nil {
			continue
		}
		if !slices.Contains(out, ep) {
			out = append(out, ep)
			if len(out) >= 8 {
				break
			}
		}
	}
	return out
}

func (h *relayHostConn) enqueue(typ byte, payload []byte) bool {
	select {
	case <-h.closedCh:
		return false
	default:
	}
	select {
	case h.sendCh <- relayOutFrame{typ: typ, payload: payload}:
		return true
	default:
		return false
	}
}

func (h *relayHostConn) close() {
	h.closeOnce.Do(func() {
		close(h.closedCh)
		_ = h.conn.Close()
		h.mu.Lock()
		for _, c := range h.clients {
			c.close()
		}
		h.clients = map[uint32]*relayClientPeer{}
		h.mu.Unlock()
	})
}

func (h *relayHostConn) writeLoop() {
	defer h.close()
	for {
		select {
		case f := <-h.sendCh:
			_ = h.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := writeRelayFrame(h.conn, f.typ, f.payload); err != nil {
				return
			}
		case <-h.closedCh:
			return
		}
	}
}

func (c *relayClientPeer) enqueue(typ byte, payload []byte) bool {
	select {
	case <-c.closedCh:
		return false
	default:
	}
	select {
	case c.sendCh <- relayOutFrame{typ: typ, payload: payload}:
		return true
	default:
		return false
	}
}

func (c *relayClientPeer) close() {
	c.closeOnce.Do(func() {
		close(c.closedCh)
		_ = c.conn.Close()
	})
}

func (c *relayClientPeer) writeLoop() {
	defer c.close()
	for {
		select {
		case f := <-c.sendCh:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := writeRelayFrame(c.conn, f.typ, f.payload); err != nil {
				return
			}
		case <-c.closedCh:
			return
		}
	}
}

func (s *RelayServer) handleHost(conn net.Conn, payload []byte) {
	var reg hostRegPayload
	if err := json.Unmarshal(payload, &reg); err != nil {
		s.sendErr(conn, "主机注册格式错误")
		return
	}
	if !s.checkToken(reg.Token) {
		s.sendErr(conn, "中继令牌无效")
		return
	}
	hostID := strings.ToLower(strings.TrimSpace(reg.HostID))
	if len(hostID) != 32 {
		s.sendErr(conn, "主机 ID 无效")
		return
	}

	h := &relayHostConn{
		hostID:    hostID,
		conn:      conn,
		sendCh:    make(chan relayOutFrame, 512),
		endpoints: cleanEndpointList(reg.Endpoints),
		clients:   map[uint32]*relayClientPeer{},
		closedCh:  make(chan struct{}),
	}

	s.mu.Lock()
	old := s.hosts[hostID]
	s.hosts[hostID] = h
	s.mu.Unlock()
	if old != nil {
		old.close()
	}

	defer func() {
		h.close()
		s.mu.Lock()
		if s.hosts[hostID] == h {
			delete(s.hosts, hostID)
		}
		s.mu.Unlock()
	}()

	obsIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	okBody, _ := json.Marshal(map[string]any{"ok": true, "observed_ip": obsIP})
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := writeRelayFrame(conn, relayMsgHostOK, okBody); err != nil {
		return
	}
	go h.writeLoop()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(65 * time.Second))
		typ, body, err := readRelayFrame(conn)
		if err != nil {
			return
		}
		switch typ {
		case relayMsgHostUpdate:
			var upd struct {
				Endpoints []string `json:"endpoints"`
			}
			if json.Unmarshal(body, &upd) == nil {
				h.mu.Lock()
				h.endpoints = cleanEndpointList(upd.Endpoints)
				h.mu.Unlock()
			}
			h.enqueue(relayMsgPong, nil)

		case relayMsgPing:
			h.enqueue(relayMsgPong, nil)

		case relayMsgPacket:
			connID, pkt, ok := decodeRelayPacketPayload(body)
			if !ok {
				continue
			}
			h.mu.RLock()
			cp := h.clients[connID]
			h.mu.RUnlock()
			if cp != nil {
				cp.enqueue(relayMsgPacket, encodeRelayPacketPayload(connID, pkt))
			}
		}
	}
}

func (s *RelayServer) handleClient(conn net.Conn, payload []byte) {
	var join clientJoinPayload
	if err := json.Unmarshal(payload, &join); err != nil {
		s.sendErr(conn, "客户端请求格式错误")
		return
	}
	if !s.checkToken(join.Token) {
		s.sendErr(conn, "中继令牌无效")
		return
	}
	hostID := strings.ToLower(strings.TrimSpace(join.HostID))
	s.mu.RLock()
	h := s.hosts[hostID]
	s.mu.RUnlock()
	if h == nil {
		s.sendErr(conn, "主机当前不在线（未连接到中继服务器）")
		return
	}

	connID := s.nextConnID.Add(1)
	if connID == 0 {
		connID = s.nextConnID.Add(1)
	}
	cp := &relayClientPeer{
		connID:   connID,
		conn:     conn,
		sendCh:   make(chan relayOutFrame, 256),
		closedCh: make(chan struct{}),
	}

	h.mu.Lock()
	hostEps := append([]string(nil), h.endpoints...)
	h.clients[connID] = cp
	h.mu.Unlock()

	defer func() {
		cp.close()
		h.mu.Lock()
		delete(h.clients, connID)
		h.mu.Unlock()
		var cidBuf [4]byte
		binary.BigEndian.PutUint32(cidBuf[:], connID)
		h.enqueue(relayMsgClientClosed, cidBuf[:])
	}()

	// 1. 立即把主机的最新候选端点发给手机
	infoBody, _ := json.Marshal(punchPayload{
		ConnID:    connID,
		Endpoints: hostEps,
	})
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := writeRelayFrame(conn, relayMsgHostInfo, infoBody); err != nil {
		return
	}

	// 2. 同时通知内网主机立即向手机的候选端点反向发 UDP 打洞包
	clientEps := cleanEndpointList(join.Endpoints)
	if len(clientEps) > 0 {
		punchBody, _ := json.Marshal(punchPayload{
			ConnID:    connID,
			Endpoints: clientEps,
		})
		h.enqueue(relayMsgPunch, punchBody)
	}

	go cp.writeLoop()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(95 * time.Second))
		typ, body, err := readRelayFrame(conn)
		if err != nil {
			return
		}
		switch typ {
		case relayMsgPing:
			cp.enqueue(relayMsgPong, nil)
		case relayMsgPacket:
			_, pkt, ok := decodeRelayPacketPayload(body)
			if !ok {
				continue
			}
			// 强制写入服务端分配的 connID，防止伪造其他会话
			h.enqueue(relayMsgPacket, encodeRelayPacketPayload(connID, pkt))
		}
	}
}

// ---------- 主机端中继客户端（由 p2pInbound 持有，长连接保活 + 收到 PUNCH 反向打洞 + 兜底收发密文包） ----------

type hostRelayClient struct {
	relayAddr string
	token     string
	hostID    string

	getEndpoints func() []string
	onPunch      func(endpoints []string)
	onPacket     func(connID uint32, pkt []byte)

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.RWMutex
	connected  bool
	lastErr    string
	observedIP string
	sendCh     chan relayOutFrame
	activeConn net.Conn
}

func startHostRelayClient(
	relayAddr, token, hostID string,
	getEndpoints func() []string,
	onPunch func(endpoints []string),
	onPacket func(connID uint32, pkt []byte),
) *hostRelayClient {
	ctx, cancel := context.WithCancel(context.Background())
	c := &hostRelayClient{
		relayAddr:    strings.TrimSpace(relayAddr),
		token:        strings.TrimSpace(token),
		hostID:       hostID,
		getEndpoints: getEndpoints,
		onPunch:      onPunch,
		onPacket:     onPacket,
		ctx:          ctx,
		cancel:       cancel,
	}
	go c.runLoop()
	return c
}

func (c *hostRelayClient) Close() {
	c.cancel()
	c.mu.Lock()
	if c.activeConn != nil {
		_ = c.activeConn.Close()
		c.activeConn = nil
	}
	c.connected = false
	c.mu.Unlock()
}

func (c *hostRelayClient) Status() (connected bool, lastErr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected, c.lastErr
}

func (c *hostRelayClient) SendPacket(connID uint32, pkt []byte) {
	c.mu.RLock()
	ch := c.sendCh
	c.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- relayOutFrame{typ: relayMsgPacket, payload: encodeRelayPacketPayload(connID, pkt)}:
	default:
	}
}

func (c *hostRelayClient) runLoop() {
	backoff := time.Second
	for {
		if c.ctx.Err() != nil {
			return
		}
		err := c.connectOnce()
		if c.ctx.Err() != nil {
			return
		}
		if err != nil {
			c.mu.Lock()
			c.connected = false
			c.lastErr = err.Error()
			c.mu.Unlock()
			log.Printf("[frog-relay] 与中继 %s 断开：%v，%s 后重连", c.relayAddr, err, backoff)
		}
		select {
		case <-time.After(backoff):
		case <-c.ctx.Done():
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
		}
	}
}

func (c *hostRelayClient) connectOnce() error {
	dialCtx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	conn, err := dialRelayTLS(dialCtx, c.relayAddr)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close()

	regBody, _ := json.Marshal(hostRegPayload{
		HostID:    c.hostID,
		Token:     c.token,
		Endpoints: c.getEndpoints(),
	})
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := writeRelayFrame(conn, relayMsgHostRegister, regBody); err != nil {
		return err
	}

	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	typ, body, err := readRelayFrame(conn)
	if err != nil {
		return err
	}
	if typ == relayMsgError {
		var er struct{ Error string `json:"error"` }
		_ = json.Unmarshal(body, &er)
		if er.Error == "" {
			er.Error = "中继拒绝注册"
		}
		return errors.New(er.Error)
	}
	if typ != relayMsgHostOK {
		return fmt.Errorf("中继返回未知响应: 0x%02x", typ)
	}
	var okResp struct {
		ObservedIP string `json:"observed_ip"`
	}
	_ = json.Unmarshal(body, &okResp)

	sendCh := make(chan relayOutFrame, 512)
	done := make(chan struct{})
	defer close(done)

	c.mu.Lock()
	c.activeConn = conn
	c.sendCh = sendCh
	c.connected = true
	c.lastErr = ""
	c.observedIP = okResp.ObservedIP
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.activeConn == conn {
			c.activeConn = nil
			c.sendCh = nil
			c.connected = false
		}
		c.mu.Unlock()
	}()

	// 写协程
	writeErrCh := make(chan error, 1)
	go func() {
		for {
			select {
			case f := <-sendCh:
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := writeRelayFrame(conn, f.typ, f.payload); err != nil {
					writeErrCh <- err
					return
				}
			case <-done:
				return
			case <-c.ctx.Done():
				_ = conn.Close()
				return
			}
		}
	}()

	// 定时心跳 + 上报最新 NAT/本地端点
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				b, _ := json.Marshal(map[string]any{"endpoints": c.getEndpoints()})
				select {
				case sendCh <- relayOutFrame{typ: relayMsgHostUpdate, payload: b}:
				default:
				}
			case <-done:
				return
			case <-c.ctx.Done():
				return
			}
		}
	}()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(65 * time.Second))
		rTyp, rBody, err := readRelayFrame(conn)
		if err != nil {
			select {
			case werr := <-writeErrCh:
				return werr
			default:
			}
			return err
		}
		switch rTyp {
		case relayMsgPong:
		case relayMsgPunch:
			var p punchPayload
			if json.Unmarshal(rBody, &p) == nil && len(p.Endpoints) > 0 && c.onPunch != nil {
				go c.onPunch(p.Endpoints)
			}
		case relayMsgPacket:
			if cid, pkt, ok := decodeRelayPacketPayload(rBody); ok && c.onPacket != nil {
				c.onPacket(cid, pkt)
			}
		case relayMsgClientClosed:
		case relayMsgError:
			var er struct{ Error string `json:"error"` }
			_ = json.Unmarshal(rBody, &er)
			return errors.New(er.Error)
		}
	}
}

// ---------- 手机端中继客户端（由 punchAndConnectP2P 调用：交换双向打洞端点 + 兜底密文转发） ----------

type clientRelayLink struct {
	connID        uint32
	hostEndpoints []string

	conn   net.Conn
	sendCh chan relayOutFrame
	rxCh   chan []byte

	closeOnce sync.Once
	closedCh  chan struct{}
}

func openClientRelay(ctx context.Context, relayAddr, token, hostID string, clientEndpoints []string) (*clientRelayLink, error) {
	conn, err := dialRelayTLS(ctx, relayAddr)
	if err != nil {
		return nil, err
	}
	joinBody, _ := json.Marshal(clientJoinPayload{
		HostID:    hostID,
		Token:     token,
		Endpoints: clientEndpoints,
	})
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := writeRelayFrame(conn, relayMsgClientJoin, joinBody); err != nil {
		_ = conn.Close()
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	typ, body, err := readRelayFrame(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if typ == relayMsgError {
		_ = conn.Close()
		var er struct{ Error string `json:"error"` }
		_ = json.Unmarshal(body, &er)
		if er.Error == "" {
			er.Error = "中继连接被拒绝"
		}
		return nil, errors.New(er.Error)
	}
	if typ != relayMsgHostInfo {
		_ = conn.Close()
		return nil, fmt.Errorf("中继返回异常帧: 0x%02x", typ)
	}
	var info punchPayload
	if err := json.Unmarshal(body, &info); err != nil {
		_ = conn.Close()
		return nil, err
	}

	link := &clientRelayLink{
		connID:        info.ConnID,
		hostEndpoints: info.Endpoints,
		conn:          conn,
		sendCh:        make(chan relayOutFrame, 256),
		rxCh:          make(chan []byte, 256),
		closedCh:      make(chan struct{}),
	}
	go link.writeLoop()
	go link.readLoop()
	go link.pingLoop()
	return link, nil
}

func (l *clientRelayLink) SendPacket(pkt []byte) {
	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	select {
	case <-l.closedCh:
		return
	default:
	}
	select {
	case l.sendCh <- relayOutFrame{typ: relayMsgPacket, payload: encodeRelayPacketPayload(l.connID, cp)}:
	default:
	}
}

func (l *clientRelayLink) Close() {
	l.closeOnce.Do(func() {
		close(l.closedCh)
		_ = l.conn.Close()
	})
}

func (l *clientRelayLink) writeLoop() {
	defer l.Close()
	for {
		select {
		case f := <-l.sendCh:
			_ = l.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := writeRelayFrame(l.conn, f.typ, f.payload); err != nil {
				return
			}
		case <-l.closedCh:
			return
		}
	}
}

func (l *clientRelayLink) pingLoop() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			select {
			case l.sendCh <- relayOutFrame{typ: relayMsgPing}:
			default:
			}
		case <-l.closedCh:
			return
		}
	}
}

func (l *clientRelayLink) readLoop() {
	defer func() {
		l.Close()
		close(l.rxCh)
	}()
	for {
		_ = l.conn.SetReadDeadline(time.Now().Add(95 * time.Second))
		typ, body, err := readRelayFrame(l.conn)
		if err != nil {
			return
		}
		if typ == relayMsgPacket {
			if _, pkt, ok := decodeRelayPacketPayload(body); ok {
				cp := make([]byte, len(pkt))
				copy(cp, pkt)
				select {
				case l.rxCh <- cp:
				case <-l.closedCh:
					return
				}
			}
		}
	}
}

// probeRelayUDPEcho 用给定 UDP 套接字向中继同端口发一次极简 Echo 探测，获取真实 NAT 映射地址。
func probeRelayUDPEcho(conn *net.UDPConn, relayAddr string, timeout time.Duration) string {
	raddr, err := net.ResolveUDPAddr("udp", relayAddr)
	if err != nil || conn == nil {
		return ""
	}
	var nonce [12]byte
	_, _ = rand.Read(nonce[:])
	var req [16]byte
	copy(req[0:4], relayUDPEchoReqMagic[:])
	copy(req[4:16], nonce[:])
	_, _ = conn.WriteToUDP(req[:], raddr)

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if n > 16 && [4]byte(buf[0:4]) == relayUDPEchoRespMagic && [12]byte(buf[4:16]) == nonce {
			obs := strings.TrimSpace(string(buf[16:n]))
			if _, _, err := net.SplitHostPort(obs); err == nil {
				_ = conn.SetReadDeadline(time.Time{})
				return obs
			}
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	return ""
}
