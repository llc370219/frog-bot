package remote

import (
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	pktTypeInit = 0x01
	pktTypeResp = 0x02
	pktTypeData = 0x03

	frameData = 0x10
	frameAck  = 0x11
	frameFin  = 0x12
	framePing = 0x13

	maxChunkPayload = 1140
	arqWindowSegs   = 128
	stunMagicCookie = 0x2112A442
)

var defaultSTUNServers = []string{
	"stun.cloudflare.com:3478",
	"stun.l.google.com:19302",
}

type P2PPeerCredential struct {
	DevID   string
	PeerID  [8]byte
	PeerPSK [32]byte
}

type P2PClientOptions struct {
	PeerIDHex    string
	PeerPSKHex   string
	ServerPubB64 string
	Candidates   []string
	RelayAddr    string
	RelayToken   string
	ServerName   string
}

func BuildP2PURI(opts P2PClientOptions) string {
	primary := "127.0.0.1:51820"
	if len(opts.Candidates) > 0 {
		primary = opts.Candidates[0]
	} else if strings.TrimSpace(opts.RelayAddr) != "" {
		primary = strings.TrimSpace(opts.RelayAddr)
	}
	pskToken := opts.PeerPSKHex
	if rawPSK, err := hex.DecodeString(opts.PeerPSKHex); err == nil && len(rawPSK) == 32 {
		pskToken = base64.RawURLEncoding.EncodeToString(rawPSK)
	}

	rawQuery := "pub=" + opts.ServerPubB64
	if r := strings.TrimSpace(opts.RelayAddr); r != "" {
		rawQuery += "&r=" + url.QueryEscape(r)
	}
	if rt := strings.TrimSpace(opts.RelayToken); rt != "" {
		rawQuery += "&rt=" + url.QueryEscape(rt)
	}
	if len(opts.Candidates) > 1 {
		var extras []string
		// 为 QR Version 12 (366B) 留足余量，最多控制 URI 总长 <= 325 字节
		curLen := 72 + len(primary) + len(rawQuery) + len(opts.ServerName)*3
		maxExtras := 5
		if strings.TrimSpace(opts.RelayAddr) != "" {
			// 有中继信令实时下发最新地址，二维码里只需附带最多 2 个本地/直连地址做局域网秒连加速
			maxExtras = 2
		}
		for _, ep := range opts.Candidates[1:] {
			if len(extras) >= maxExtras || curLen+len(ep)+4 > 325 {
				break
			}
			extras = append(extras, ep)
			curLen += len(ep) + 1
		}
		if len(extras) > 0 {
			rawQuery += "&ep=" + strings.Join(extras, ",")
		}
	}
	u := &url.URL{
		Scheme:   "frogp2p",
		User:     url.UserPassword(opts.PeerIDHex, pskToken),
		Host:     primary,
		RawQuery: rawQuery,
		Fragment: opts.ServerName,
	}
	return u.String()
}

func ParseP2PURI(raw string) (P2PClientOptions, error) {
	s := strings.TrimSpace(raw)
	if idx := strings.Index(strings.ToLower(s), "frogp2p://"); idx >= 0 {
		s = s[idx:]
		if end := strings.IndexAny(s, " \t\r\n"); end >= 0 {
			s = s[:end]
		}
	} else {
		return P2PClientOptions{}, errors.New("未识别到 frogp2p:// 链接")
	}
	u, err := url.Parse(s)
	if err != nil {
		return P2PClientOptions{}, fmt.Errorf("无效的 P2P 链接: %w", err)
	}
	if u.User == nil {
		return P2PClientOptions{}, errors.New("P2P 链接缺少设备密钥")
	}
	peerID := strings.ToLower(strings.TrimSpace(u.User.Username()))
	pskRaw, _ := u.User.Password()
	pskRaw = strings.TrimSpace(pskRaw)
	if b, err := hex.DecodeString(peerID); err != nil || len(b) != 8 {
		return P2PClientOptions{}, errors.New("P2P PeerID 无效")
	}
	var pskHex string
	if b, err := hex.DecodeString(pskRaw); err == nil && len(b) == 32 {
		pskHex = strings.ToLower(pskRaw)
	} else if b, err := decodeBase64Flexible(pskRaw); err == nil && len(b) == 32 {
		pskHex = hex.EncodeToString(b)
	} else {
		return P2PClientOptions{}, errors.New("P2P PSK 密钥无效")
	}
	pub := strings.TrimSpace(u.Query().Get("pub"))
	pubBytes, err := decodeBase64Flexible(pub)
	if err != nil || len(pubBytes) != 32 {
		return P2PClientOptions{}, errors.New("P2P 公钥无效")
	}
	var cands []string
	if u.Host != "" {
		if _, _, err := net.SplitHostPort(u.Host); err == nil {
			cands = append(cands, u.Host)
		}
	}
	for _, extra := range strings.Split(u.Query().Get("ep"), ",") {
		extra = strings.TrimSpace(extra)
		if extra != "" && !slices.Contains(cands, extra) {
			if _, _, err := net.SplitHostPort(extra); err == nil {
				cands = append(cands, extra)
			}
		}
	}
	relayAddr := strings.TrimSpace(u.Query().Get("r"))
	relayToken := strings.TrimSpace(u.Query().Get("rt"))
	if len(cands) == 0 && relayAddr == "" {
		return P2PClientOptions{}, errors.New("P2P 链接缺少候选端点")
	}
	name := strings.TrimSpace(u.Fragment)
	if decoded, err := url.PathUnescape(name); err == nil && decoded != "" {
		name = decoded
	}
	return P2PClientOptions{
		PeerIDHex:    peerID,
		PeerPSKHex:   pskHex,
		ServerPubB64: base64.RawURLEncoding.EncodeToString(pubBytes),
		Candidates:   cands,
		RelayAddr:    relayAddr,
		RelayToken:   relayToken,
		ServerName:   name,
	}, nil
}

// ---------- RFC 5389 STUN (共用同一 UDP 套接字以获取真实 NAT 映射端口) ----------

type pendingSTUN struct {
	txID [12]byte
	ch   chan string
}

func parseSTUNBindingResponse(pkt []byte, wantTxID [12]byte) (string, bool) {
	if len(pkt) < 20 {
		return "", false
	}
	msgType := binary.BigEndian.Uint16(pkt[0:2])
	msgLen := int(binary.BigEndian.Uint16(pkt[2:4]))
	cookie := binary.BigEndian.Uint32(pkt[4:8])
	if msgType != 0x0101 || cookie != stunMagicCookie || len(pkt) < 20+msgLen {
		return "", false
	}
	var gotTx [12]byte
	copy(gotTx[:], pkt[8:20])
	if gotTx != wantTxID {
		return "", false
	}
	attrs := pkt[20 : 20+msgLen]
	for len(attrs) >= 4 {
		attrType := binary.BigEndian.Uint16(attrs[0:2])
		attrLen := int(binary.BigEndian.Uint16(attrs[2:4]))
		if len(attrs) < 4+attrLen {
			break
		}
		val := attrs[4 : 4+attrLen]
		// XOR-MAPPED-ADDRESS (0x0020) or MAPPED-ADDRESS (0x0001)
		if (attrType == 0x0020 || attrType == 0x0001) && len(val) >= 8 {
			family := val[1]
			port := binary.BigEndian.Uint16(val[2:4])
			if attrType == 0x0020 {
				port ^= uint16(stunMagicCookie >> 16)
			}
			if family == 0x01 && len(val) >= 8 {
				var ip [4]byte
				copy(ip[:], val[4:8])
				if attrType == 0x0020 {
					var c [4]byte
					binary.BigEndian.PutUint32(c[:], stunMagicCookie)
					for i := 0; i < 4; i++ {
						ip[i] ^= c[i]
					}
				}
				return net.JoinHostPort(net.IP(ip[:]).String(), fmt.Sprintf("%d", port)), true
			} else if family == 0x02 && len(val) >= 20 {
				var ip [16]byte
				copy(ip[:], val[4:20])
				if attrType == 0x0020 {
					var mask [16]byte
					binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
					copy(mask[4:16], wantTxID[:])
					for i := 0; i < 16; i++ {
						ip[i] ^= mask[i]
					}
				}
				return net.JoinHostPort(net.IP(ip[:]).String(), fmt.Sprintf("%d", port)), true
			}
		}
		padded := (attrLen + 3) &^ 3
		if len(attrs) < 4+padded {
			break
		}
		attrs = attrs[4+padded:]
	}
	return "", false
}

// ---------- Server: WireGuard-style UDP P2P Inbound ----------

type p2pInbound struct {
	conn       *net.UDPConn
	port       int
	staticPriv *ecdh.PrivateKey
	router     func(ctx context.Context, devID string, conn net.Conn, dest TargetAddr)

	relayAddr  string
	relayToken string
	relay      *hostRelayClient

	mu         sync.RWMutex
	peers      map[[8]byte]P2PPeerCredential
	sessions   map[uint64]*arqSession
	stunWaiter *pendingSTUN
	stunAddr   string

	ctx    context.Context
	cancel context.CancelFunc
}

func startP2PInbound(listen, privB64, pubB64, relayAddr, relayToken string, router func(ctx context.Context, devID string, conn net.Conn, dest TargetAddr)) (*p2pInbound, error) {
	privBytes, err := decodeBase64Flexible(privB64)
	if err != nil || len(privBytes) != 32 {
		return nil, errors.New("P2P 私钥无效")
	}
	priv, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		return nil, err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", normalizeListenAddr(listen, ":51820"))
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("UDP 监听 %s 失败: %w", listen, err)
	}
	_ = conn.SetReadBuffer(2 << 20)
	_ = conn.SetWriteBuffer(2 << 20)

	port := 0
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		port = ua.Port
	}
	ctx, cancel := context.WithCancel(context.Background())
	inb := &p2pInbound{
		conn:       conn,
		port:       port,
		staticPriv: priv,
		router:     router,
		relayAddr:  strings.TrimSpace(relayAddr),
		relayToken: strings.TrimSpace(relayToken),
		peers:      map[[8]byte]P2PPeerCredential{},
		sessions:   map[uint64]*arqSession{},
		ctx:        ctx,
		cancel:     cancel,
	}
	go inb.readLoop()
	go inb.stunKeepaliveLoop()

	if inb.relayAddr != "" {
		hostID := HostIDFromP2PPubKey(pubB64)
		if hostID != "" {
			inb.relay = startHostRelayClient(
				inb.relayAddr,
				inb.relayToken,
				hostID,
				func() []string { return inb.CandidateEndpoints("") },
				inb.punchClientEndpoints,
				inb.onRelayPacket,
			)
		}
	}
	return inb, nil
}

func (p *p2pInbound) Port() int { return p.port }

func (p *p2pInbound) RelayStatus() (configured bool, connected bool, lastErr string) {
	if p.relay == nil {
		return false, false, ""
	}
	conn, errStr := p.relay.Status()
	return true, conn, errStr
}

func (p *p2pInbound) Close() error {
	p.cancel()
	if p.relay != nil {
		p.relay.Close()
	}
	p.mu.Lock()
	for _, s := range p.sessions {
		_ = s.Close()
	}
	p.sessions = map[uint64]*arqSession{}
	p.mu.Unlock()
	return p.conn.Close()
}

// punchClientEndpoints 收到中继信令后，立即向手机候选端点反向发送 UDP 打洞包打开本地路由器 NAT 映射。
func (p *p2pInbound) punchClientEndpoints(endpoints []string) {
	var addrs []*net.UDPAddr
	for _, ep := range endpoints {
		if ua, err := net.ResolveUDPAddr("udp", ep); err == nil {
			addrs = append(addrs, ua)
		}
	}
	if len(addrs) == 0 {
		return
	}
	var punch [16]byte
	copy(punch[0:8], p2pPunchMagic[:])
	_, _ = rand.Read(punch[8:16])
	for round := 0; round < 4; round++ {
		if p.ctx.Err() != nil {
			return
		}
		for _, ua := range addrs {
			_, _ = p.conn.WriteToUDP(punch[:], ua)
		}
		select {
		case <-time.After(140 * time.Millisecond):
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *p2pInbound) sendPacket(b []byte, to net.Addr) {
	switch a := to.(type) {
	case relayPeerAddr:
		if p.relay != nil {
			p.relay.SendPacket(a.connID, b)
		}
	case *net.UDPAddr:
		_, _ = p.conn.WriteToUDP(b, a)
	default:
		if to != nil {
			_, _ = p.conn.WriteTo(b, to)
		}
	}
}

func (p *p2pInbound) onRelayPacket(connID uint32, pkt []byte) {
	if len(pkt) < 16 {
		return
	}
	addr := relayPeerAddr{connID: connID}
	switch pkt[0] {
	case pktTypeInit:
		p.handleInit(pkt, addr)
	case pktTypeData:
		if len(pkt) < 25 {
			return
		}
		sessID := binary.BigEndian.Uint64(pkt[1:9])
		p.mu.RLock()
		sess := p.sessions[sessID]
		p.mu.RUnlock()
		if sess != nil {
			sess.onEncryptedPacket(pkt, addr)
		}
	}
}

func (p *p2pInbound) UpdatePeers(list []P2PPeerCredential) {
	next := make(map[[8]byte]P2PPeerCredential, len(list))
	validDevs := make(map[string]bool, len(list))
	for _, c := range list {
		next[c.PeerID] = c
		validDevs[c.DevID] = true
	}
	p.mu.Lock()
	p.peers = next
	for id, sess := range p.sessions {
		if !validDevs[sess.devID] {
			_ = sess.Close()
			delete(p.sessions, id)
		}
	}
	p.mu.Unlock()
}

func (p *p2pInbound) CandidateEndpoints(publicHost string) []string {
	var out []string
	if h := cleanHostInput(publicHost); h != "" && p.port > 0 {
		out = append(out, net.JoinHostPort(h, fmt.Sprintf("%d", p.port)))
	}
	p.mu.RLock()
	stunEp := p.stunAddr
	p.mu.RUnlock()
	if stunEp == "" {
		stunEp = p.RefreshSTUN(900 * time.Millisecond)
	}
	if stunEp != "" && !slices.Contains(out, stunEp) {
		out = append(out, stunEp)
	}
	for _, ep := range discoverLocalEndpoints(p.port) {
		if !slices.Contains(out, ep) {
			out = append(out, ep)
		}
	}
	if len(out) == 0 && p.port > 0 {
		out = append(out, fmt.Sprintf("127.0.0.1:%d", p.port))
	}
	return out
}

func (p *p2pInbound) RefreshSTUN(timeout time.Duration) string {
	var txID [12]byte
	_, _ = rand.Read(txID[:])
	waiter := &pendingSTUN{txID: txID, ch: make(chan string, 1)}
	p.mu.Lock()
	p.stunWaiter = waiter
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.stunWaiter == waiter {
			p.stunWaiter = nil
		}
		p.mu.Unlock()
	}()

	// 优先向自建中继同端口发送极简 UDP Echo 探测（零第三方 STUN 依赖）
	if p.relayAddr != "" {
		if raddr, err := net.ResolveUDPAddr("udp", p.relayAddr); err == nil {
			var echo [16]byte
			copy(echo[0:4], relayUDPEchoReqMagic[:])
			copy(echo[4:16], txID[:])
			_, _ = p.conn.WriteToUDP(echo[:], raddr)
		}
	}

	var req [20]byte
	binary.BigEndian.PutUint16(req[0:2], 0x0001)
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:20], txID[:])

	for _, srv := range defaultSTUNServers {
		raddr, err := net.ResolveUDPAddr("udp4", srv)
		if err != nil {
			continue
		}
		_, _ = p.conn.WriteToUDP(req[:], raddr)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case mapped := <-waiter.ch:
		p.mu.Lock()
		p.stunAddr = mapped
		p.mu.Unlock()
		return mapped
	case <-timer.C:
		return ""
	case <-p.ctx.Done():
		return ""
	}
}

func (p *p2pInbound) stunKeepaliveLoop() {
	_ = p.RefreshSTUN(1200 * time.Millisecond)
	t := time.NewTicker(25 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = p.RefreshSTUN(1200 * time.Millisecond)
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *p2pInbound) readLoop() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			continue
		}
		if n < 16 {
			continue
		}
		pkt := buf[:n]
		if n > 16 && [4]byte(pkt[0:4]) == relayUDPEchoRespMagic {
			p.mu.RLock()
			w := p.stunWaiter
			p.mu.RUnlock()
			if w != nil && [12]byte(pkt[4:16]) == w.txID {
				obs := strings.TrimSpace(string(pkt[16:n]))
				if _, _, err := net.SplitHostPort(obs); err == nil {
					select {
					case w.ch <- obs:
					default:
					}
				}
			}
			continue
		}
		if n >= 20 && binary.BigEndian.Uint32(pkt[4:8]) == stunMagicCookie {
			p.mu.RLock()
			w := p.stunWaiter
			p.mu.RUnlock()
			if w != nil {
				if mapped, ok := parseSTUNBindingResponse(pkt, w.txID); ok {
					select {
					case w.ch <- mapped:
					default:
					}
				}
			}
			continue
		}

		switch pkt[0] {
		case pktTypeInit:
			p.handleInit(pkt, addr)
		case pktTypeData:
			if len(pkt) < 25 {
				continue
			}
			sessID := binary.BigEndian.Uint64(pkt[1:9])
			p.mu.RLock()
			sess := p.sessions[sessID]
			p.mu.RUnlock()
			if sess != nil {
				sess.onEncryptedPacket(pkt, addr)
			}
		}
	}
}

func (p *p2pInbound) handleInit(pkt []byte, addr net.Addr) {
	// [0x01][peerID:8][clientSessID:8][cEphPub:32][sealed(ts:8 + rand:8) = 32] => 81 bytes
	if len(pkt) != 81 {
		return
	}
	var peerID [8]byte
	copy(peerID[:], pkt[1:9])

	p.mu.RLock()
	cred, ok := p.peers[peerID]
	p.mu.RUnlock()
	if !ok {
		return // 静默丢弃未知探测包，零端口指纹暴露
	}

	clientSessID := binary.BigEndian.Uint64(pkt[9:17])
	cPub, err := ecdh.X25519().NewPublicKey(pkt[17:49])
	if err != nil {
		return
	}
	staticShared, err := p.staticPriv.ECDH(cPub)
	if err != nil {
		return
	}
	initKey := deriveKey32(staticShared, cred.PeerPSK[:], "frogp2p-init")
	aead, err := chacha20poly1305.New(initKey[:])
	if err != nil {
		return
	}
	var zeroNonce [12]byte
	binary.BigEndian.PutUint64(zeroNonce[4:12], clientSessID)
	plain, err := aead.Open(nil, zeroNonce[:], pkt[49:81], pkt[:49])
	if err != nil || len(plain) != 16 {
		return
	}
	tsSec := int64(binary.BigEndian.Uint64(plain[0:8]))
	diff := time.Now().Unix() - tsSec
	if diff < -300 || diff > 300 {
		return
	}

	sEphPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return
	}
	ephShared, err := sEphPriv.ECDH(cPub)
	if err != nil {
		return
	}
	var serverSessBytes [8]byte
	_, _ = rand.Read(serverSessBytes[:])
	serverSessID := binary.BigEndian.Uint64(serverSessBytes[:])

	_, isRelay := addr.(relayPeerAddr)
	c2sKey, s2cKey := deriveDirectionalKeys(staticShared, ephShared, cred.PeerPSK[:], clientSessID, serverSessID)
	sess, err := newARQSession(arqConfig{
		devID:      cred.DevID,
		localID:    serverSessID,
		remoteID:   clientSessID,
		txKey:      s2cKey,
		rxKey:      c2sKey,
		sendUDP:    p.sendPacket,
		remoteAddr: addr,
		viaRelay:   isRelay,
	})
	if err != nil {
		return
	}

	p.mu.Lock()
	if len(p.sessions) >= 128 {
		// 清理最久未活跃会话
		for k, old := range p.sessions {
			_ = old.Close()
			delete(p.sessions, k)
			break
		}
	}
	p.sessions[serverSessID] = sess
	p.mu.Unlock()

	go p.serveMuxSession(serverSessID, sess)

	// 构造 Resp: [0x02][clientSessID:8][serverSessID:8][sEphPub:32][sealedAck(plain[8:16]) = 24] => 73 bytes
	resp := make([]byte, 49, 73)
	resp[0] = pktTypeResp
	binary.BigEndian.PutUint64(resp[1:9], clientSessID)
	binary.BigEndian.PutUint64(resp[9:17], serverSessID)
	copy(resp[17:49], sEphPriv.PublicKey().Bytes())

	respKey := deriveKey32(ephShared, cred.PeerPSK[:], "frogp2p-resp")
	respAEAD, _ := chacha20poly1305.New(respKey[:])
	var rNonce [12]byte
	binary.BigEndian.PutUint64(rNonce[4:12], serverSessID)
	resp = respAEAD.Seal(resp, rNonce[:], plain[8:16], resp[:49])
	p.sendPacket(resp, addr)
}

func (p *p2pInbound) serveMuxSession(sessID uint64, sess *arqSession) {
	defer func() {
		_ = sess.Close()
		p.mu.Lock()
		delete(p.sessions, sessID)
		p.mu.Unlock()
	}()
	mcfg := yamux.DefaultConfig()
	mcfg.EnableKeepAlive = true
	mcfg.KeepAliveInterval = 20 * time.Second
	mcfg.LogOutput = io.Discard
	mux, err := yamux.Server(sess, mcfg)
	if err != nil {
		return
	}
	defer mux.Close()

	for {
		stream, err := mux.Accept()
		if err != nil {
			return
		}
		go func(st net.Conn) {
			defer st.Close()
			_ = st.SetReadDeadline(time.Now().Add(8 * time.Second))
			var lenBuf [2]byte
			if _, err := io.ReadFull(st, lenBuf[:]); err != nil {
				return
			}
			addrLen := int(binary.BigEndian.Uint16(lenBuf[:]))
			if addrLen == 0 || addrLen > 512 {
				return
			}
			addrBytes := make([]byte, addrLen)
			if _, err := io.ReadFull(st, addrBytes); err != nil {
				return
			}
			_ = st.SetReadDeadline(time.Time{})
			p.router(p.ctx, sess.devID, st, ParseTargetAddr(string(addrBytes)))
		}(stream)
	}
}

// ---------- Client: UDP Hole-Punching + Multiplexed Session Pool ----------

type p2pOutbound struct {
	opts P2PClientOptions

	mu      sync.Mutex
	mux     *yamux.Session
	udpConn *net.UDPConn
	relay   *clientRelayLink
	arq     *arqSession
	closed  bool
}

func newP2POutbound(opts P2PClientOptions) *p2pOutbound {
	return &p2pOutbound{opts: opts}
}

func (c *p2pOutbound) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.mux != nil {
		_ = c.mux.Close()
		c.mux = nil
	}
	if c.arq != nil {
		_ = c.arq.Close()
		c.arq = nil
	}
	if c.udpConn != nil {
		_ = c.udpConn.Close()
		c.udpConn = nil
	}
	if c.relay != nil {
		c.relay.Close()
		c.relay = nil
	}
	return nil
}

func (c *p2pOutbound) DialContext(ctx context.Context, targetAddr string) (net.Conn, error) {
	mux, err := c.ensureMux(ctx)
	if err != nil {
		return nil, err
	}
	st, err := mux.Open()
	if err != nil {
		// 会话已断，重连一次
		c.mu.Lock()
		if c.mux == mux {
			_ = c.mux.Close()
			c.mux = nil
			if c.arq != nil {
				_ = c.arq.Close()
				c.arq = nil
			}
			if c.udpConn != nil {
				_ = c.udpConn.Close()
				c.udpConn = nil
			}
			if c.relay != nil {
				c.relay.Close()
				c.relay = nil
			}
		}
		c.mu.Unlock()

		mux, err = c.ensureMux(ctx)
		if err != nil {
			return nil, err
		}
		st, err = mux.Open()
		if err != nil {
			return nil, err
		}
	}

	hdr := make([]byte, 2+len(targetAddr))
	binary.BigEndian.PutUint16(hdr[0:2], uint16(len(targetAddr)))
	copy(hdr[2:], targetAddr)
	if _, err := st.Write(hdr); err != nil {
		_ = st.Close()
		return nil, err
	}
	return st, nil
}

func (c *p2pOutbound) ensureMux(ctx context.Context) (*yamux.Session, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	if c.mux != nil && !c.mux.IsClosed() {
		m := c.mux
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()

	udpConn, relayLink, arq, mux, err := punchAndConnectP2P(ctx, c.opts)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = mux.Close()
		_ = arq.Close()
		if udpConn != nil {
			_ = udpConn.Close()
		}
		if relayLink != nil {
			relayLink.Close()
		}
		return nil, net.ErrClosed
	}
	if c.mux != nil && !c.mux.IsClosed() {
		_ = mux.Close()
		_ = arq.Close()
		if udpConn != nil {
			_ = udpConn.Close()
		}
		if relayLink != nil {
			relayLink.Close()
		}
		return c.mux, nil
	}
	c.udpConn = udpConn
	c.relay = relayLink
	c.arq = arq
	c.mux = mux
	return mux, nil
}

type rxPkt struct {
	pkt      []byte
	from     net.Addr
	viaRelay bool
}

func punchAndConnectP2P(ctx context.Context, opts P2PClientOptions) (*net.UDPConn, *clientRelayLink, *arqSession, *yamux.Session, error) {
	var peerID [8]byte
	var peerPSK [32]byte
	if _, err := hex.Decode(peerID[:], []byte(opts.PeerIDHex)); err != nil {
		return nil, nil, nil, nil, err
	}
	if _, err := hex.Decode(peerPSK[:], []byte(opts.PeerPSKHex)); err != nil {
		return nil, nil, nil, nil, err
	}
	sPubBytes, err := decodeBase64Flexible(opts.ServerPubB64)
	if err != nil || len(sPubBytes) != 32 {
		return nil, nil, nil, nil, errors.New("P2P 服务端公钥无效")
	}
	sStaticPub, err := ecdh.X25519().NewPublicKey(sPubBytes)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	var resolvedMu sync.Mutex
	var resolved []*net.UDPAddr
	addResolved := func(list []string) {
		resolvedMu.Lock()
		defer resolvedMu.Unlock()
		for _, ep := range list {
			ua, err := net.ResolveUDPAddr("udp", ep)
			if err != nil {
				continue
			}
			dup := false
			for _, ex := range resolved {
				if ex.String() == ua.String() {
					dup = true
					break
				}
			}
			if !dup {
				resolved = append(resolved, ua)
			}
		}
	}
	addResolved(opts.Candidates)

	if len(resolved) == 0 && strings.TrimSpace(opts.RelayAddr) == "" {
		return nil, nil, nil, nil, errors.New("无法解析 P2P 候选地址")
	}

	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil && strings.TrimSpace(opts.RelayAddr) == "" {
		return nil, nil, nil, nil, err
	}
	if udpConn != nil {
		_ = udpConn.SetReadBuffer(2 << 20)
		_ = udpConn.SetWriteBuffer(2 << 20)
	}

	cEphPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		if udpConn != nil {
			_ = udpConn.Close()
		}
		return nil, nil, nil, nil, err
	}
	staticShared, err := cEphPriv.ECDH(sStaticPub)
	if err != nil {
		if udpConn != nil {
			_ = udpConn.Close()
		}
		return nil, nil, nil, nil, err
	}

	var cSessBytes [8]byte
	_, _ = rand.Read(cSessBytes[:])
	clientSessID := binary.BigEndian.Uint64(cSessBytes[:])

	var challenge [8]byte
	_, _ = rand.Read(challenge[:])
	var plain [16]byte
	binary.BigEndian.PutUint64(plain[0:8], uint64(time.Now().Unix()))
	copy(plain[8:16], challenge[:])

	initPkt := make([]byte, 49, 81)
	initPkt[0] = pktTypeInit
	copy(initPkt[1:9], peerID[:])
	binary.BigEndian.PutUint64(initPkt[9:17], clientSessID)
	copy(initPkt[17:49], cEphPriv.PublicKey().Bytes())

	initKey := deriveKey32(staticShared, peerPSK[:], "frogp2p-init")
	initAEAD, _ := chacha20poly1305.New(initKey[:])
	var iNonce [12]byte
	binary.BigEndian.PutUint64(iNonce[4:12], clientSessID)
	initPkt = initAEAD.Seal(initPkt, iNonce[:], plain[:], initPkt[:49])

	hctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	// 1. 若配置了公网中继：先做极快同端口 UDP Echo 探测手机 5G NAT 映射，再连中继触发双向同步打洞 + 准备中继兜底
	var relayLink *clientRelayLink
	var relayErr error
	if relayAddr := strings.TrimSpace(opts.RelayAddr); relayAddr != "" {
		var clientEps []string
		if udpConn != nil {
			if mapped := probeRelayUDPEcho(udpConn, relayAddr, 450*time.Millisecond); mapped != "" {
				clientEps = append(clientEps, mapped)
			}
			if ua, ok := udpConn.LocalAddr().(*net.UDPAddr); ok && ua.Port > 0 {
				clientEps = append(clientEps, discoverLocalEndpoints(ua.Port)...)
			}
		}
		hostID := HostIDFromP2PPubKey(opts.ServerPubB64)
		rctx, rcancel := context.WithTimeout(hctx, 6*time.Second)
		relayLink, relayErr = openClientRelay(rctx, relayAddr, opts.RelayToken, hostID, clientEps)
		rcancel()
		if relayLink != nil && len(relayLink.hostEndpoints) > 0 {
			addResolved(relayLink.hostEndpoints)
		}
	}

	// 2. 并发向所有候选端点（本地 / 公网 IPv6 / 信令换回的主机最新 NAT 端口）发 UDP 打洞握手包
	sendUDPBurst := func() int {
		if udpConn == nil {
			return 0
		}
		resolvedMu.Lock()
		list := append([]*net.UDPAddr(nil), resolved...)
		resolvedMu.Unlock()
		for _, ua := range list {
			_, _ = udpConn.WriteToUDP(initPkt, ua)
		}
		return len(list)
	}

	udpSentCount := sendUDPBurst()
	var relayInitSentNs atomic.Int64
	udpStartAt := time.Now()
	if relayLink != nil && udpSentCount == 0 {
		relayInitSentNs.Store(time.Now().UnixNano())
		relayLink.SendPacket(initPkt)
	}

	// 统一收包通道（同时监听 UDP 直连回包 与 中继兜底回包，谁先到谁胜出）
	rxCh := make(chan rxPkt, 64)
	stopReaders := make(chan struct{})
	var udpReaderDone chan struct{}
	if udpConn != nil {
		udpReaderDone = make(chan struct{})
		go func() {
			defer close(udpReaderDone)
			rb := make([]byte, 2048)
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				_ = udpConn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
				n, raddr, err := udpConn.ReadFromUDP(rb)
				if err != nil {
					var ne net.Error
					if errors.As(err, &ne) && ne.Timeout() {
						continue
					}
					return
				}
				if n >= 16 {
					cp := make([]byte, n)
					copy(cp, rb[:n])
					select {
					case rxCh <- rxPkt{pkt: cp, from: raddr, viaRelay: false}:
					case <-stopReaders:
						return
					}
				}
			}
		}()
	}

	// 若中继已连上，启动转发协程，且在 1.2s 内若 UDP 未打通则自动经由中继发出加密 Init 兜底
	stopPunch := make(chan struct{})
	punchDone := make(chan struct{})
	go func(rl *clientRelayLink) {
		defer close(punchDone)
		udpTick := time.NewTicker(220 * time.Millisecond)
		defer udpTick.Stop()
		var fallbackTimer <-chan time.Time
		if rl != nil && relayInitSentNs.Load() == 0 {
			ft := time.NewTimer(1200 * time.Millisecond)
			defer ft.Stop()
			fallbackTimer = ft.C
		}
		var relayRetry *time.Ticker
		var relayRetryCh <-chan time.Time
		defer func() {
			if relayRetry != nil {
				relayRetry.Stop()
			}
		}()
		for {
			select {
			case <-udpTick.C:
				sendUDPBurst()
			case <-fallbackTimer:
				fallbackTimer = nil
				if rl != nil {
					relayInitSentNs.Store(time.Now().UnixNano())
					rl.SendPacket(initPkt)
					relayRetry = time.NewTicker(900 * time.Millisecond)
					relayRetryCh = relayRetry.C
				}
			case <-relayRetryCh:
				if rl != nil {
					rl.SendPacket(initPkt)
				}
			case <-stopPunch:
				return
			case <-hctx.Done():
				return
			}
		}
	}(relayLink)

	var relayRxCh <-chan []byte
	if relayLink != nil {
		relayRxCh = relayLink.rxCh
	}

	var sess *arqSession
	var wonViaRelay bool
	for sess == nil {
		var pkt []byte
		var from net.Addr
		var viaRelay bool

		select {
		case <-hctx.Done():
			close(stopPunch)
			close(stopReaders)
			if udpConn != nil {
				_ = udpConn.Close()
			}
			if relayLink != nil {
				relayLink.Close()
			}
			if relayErr != nil {
				return nil, nil, nil, nil, fmt.Errorf("P2P 打洞超时且中继不可用: %v", relayErr)
			}
			return nil, nil, nil, nil, errors.New("P2P 打洞连接超时")
		case item := <-rxCh:
			pkt, from, viaRelay = item.pkt, item.from, item.viaRelay
		case rp, ok := <-relayRxCh:
			if !ok {
				relayRxCh = nil
				continue
			}
			pkt, from, viaRelay = rp, relayPeerAddr{connID: relayLink.connID}, true
		}

		if len(pkt) != 73 || pkt[0] != pktTypeResp {
			continue
		}
		if binary.BigEndian.Uint64(pkt[1:9]) != clientSessID {
			continue
		}
		serverSessID := binary.BigEndian.Uint64(pkt[9:17])
		sEphPub, err := ecdh.X25519().NewPublicKey(pkt[17:49])
		if err != nil {
			continue
		}
		ephShared, err := cEphPriv.ECDH(sEphPub)
		if err != nil {
			continue
		}
		respKey := deriveKey32(ephShared, peerPSK[:], "frogp2p-resp")
		respAEAD, _ := chacha20poly1305.New(respKey[:])
		var rNonce [12]byte
		binary.BigEndian.PutUint64(rNonce[4:12], serverSessID)
		ackPlain, err := respAEAD.Open(nil, rNonce[:], pkt[49:73], pkt[:49])
		if err != nil || string(ackPlain) != string(challenge[:]) {
			continue
		}

		var initialRTT time.Duration
		if sentNs := relayInitSentNs.Load(); viaRelay && sentNs > 0 {
			initialRTT = time.Since(time.Unix(0, sentNs))
		} else {
			initialRTT = time.Since(udpStartAt)
		}

		var sendFn func([]byte, net.Addr)
		if viaRelay {
			rl := relayLink
			sendFn = func(b []byte, _ net.Addr) { rl.SendPacket(b) }
		} else {
			uc := udpConn
			sendFn = func(b []byte, to net.Addr) { _, _ = uc.WriteTo(b, to) }
		}

		c2sKey, s2cKey := deriveDirectionalKeys(staticShared, ephShared, peerPSK[:], clientSessID, serverSessID)
		sess, err = newARQSession(arqConfig{
			localID:    clientSessID,
			remoteID:   serverSessID,
			txKey:      c2sKey,
			rxKey:      s2cKey,
			sendUDP:    sendFn,
			remoteAddr: from,
			viaRelay:   viaRelay,
			initialRTT: initialRTT,
		})
		if err != nil {
			close(stopPunch)
			close(stopReaders)
			if udpConn != nil {
				_ = udpConn.Close()
			}
			if relayLink != nil {
				relayLink.Close()
			}
			return nil, nil, nil, nil, err
		}
		wonViaRelay = viaRelay
	}

	close(stopPunch)
	<-punchDone
	close(stopReaders)
	if udpConn != nil {
		_ = udpConn.SetReadDeadline(time.Now())
		if udpReaderDone != nil {
			<-udpReaderDone
		}
	}

	if wonViaRelay {
		// 中继兜底胜出：关闭多余 UDP 套接字，切入中继收包循环
		if udpConn != nil {
			_ = udpConn.Close()
			udpConn = nil
		}
		rl := relayLink
		go func() {
			for pkt := range rl.rxCh {
				if len(pkt) >= 25 && pkt[0] == pktTypeData && binary.BigEndian.Uint64(pkt[1:9]) == clientSessID {
					sess.onEncryptedPacket(pkt, relayPeerAddr{connID: rl.connID})
				}
			}
			_ = sess.Close()
		}()
	} else {
		// UDP P2P 直连胜出：立即释放中继连接，零中继流量消耗
		if relayLink != nil {
			relayLink.Close()
			relayLink = nil
		}
		_ = udpConn.SetReadDeadline(time.Time{})
		uc := udpConn
		go func() {
			rb := make([]byte, 2048)
			for {
				n, raddr, err := uc.ReadFromUDP(rb)
				if err != nil {
					_ = sess.Close()
					return
				}
				if n >= 25 && rb[0] == pktTypeData && binary.BigEndian.Uint64(rb[1:9]) == clientSessID {
					sess.onEncryptedPacket(rb[:n], raddr)
				}
			}
		}()
	}

	mcfg := yamux.DefaultConfig()
	mcfg.EnableKeepAlive = true
	mcfg.KeepAliveInterval = 20 * time.Second
	mcfg.LogOutput = io.Discard
	mux, err := yamux.Client(sess, mcfg)
	if err != nil {
		_ = sess.Close()
		if udpConn != nil {
			_ = udpConn.Close()
		}
		if relayLink != nil {
			relayLink.Close()
		}
		return nil, nil, nil, nil, err
	}
	return udpConn, relayLink, sess, mux, nil
}

// ---------- HKDF Key Derivation ----------

func deriveKey32(secret, salt []byte, info string) [32]byte {
	var out [32]byte
	_, _ = io.ReadFull(hkdf.New(sha256.New, secret, salt, []byte(info)), out[:])
	return out
}

func deriveDirectionalKeys(staticShared, ephShared, psk []byte, cID, sID uint64) (c2s, s2c [32]byte) {
	ikm := make([]byte, 0, len(staticShared)+len(ephShared)+16)
	ikm = append(ikm, staticShared...)
	ikm = append(ikm, ephShared...)
	var ids [16]byte
	binary.BigEndian.PutUint64(ids[0:8], cID)
	binary.BigEndian.PutUint64(ids[8:16], sID)
	ikm = append(ikm, ids[:]...)
	var both [64]byte
	_, _ = io.ReadFull(hkdf.New(sha256.New, ikm, psk, []byte("frogp2p-transport")), both[:])
	copy(c2s[:], both[0:32])
	copy(s2c[:], both[32:64])
	return c2s, s2c
}

// ---------- ChaCha20-Poly1305 + Selective-ACK Reliable Datagram Stream ----------

type arqConfig struct {
	devID      string
	localID    uint64
	remoteID   uint64
	txKey      [32]byte
	rxKey      [32]byte
	sendUDP    func([]byte, net.Addr)
	remoteAddr net.Addr
	viaRelay   bool
	initialRTT time.Duration
}

type txSegment struct {
	kind   byte
	seq    uint32
	data   []byte
	sentAt time.Time
	tries  int
}

type rxSegment struct {
	kind byte
	data []byte
}

type arqSession struct {
	devID    string
	localID  uint64
	remoteID uint64
	txAEAD   cipher.AEAD
	rxAEAD   cipher.AEAD
	sendUDP  func([]byte, net.Addr)
	baseRTO  time.Duration

	txPktNonce atomic.Uint64
	lastRxUnix atomic.Int64

	addrMu     sync.RWMutex
	remoteAddr net.Addr

	replayMu  sync.Mutex
	maxNonce  uint64
	replayMap uint64

	txMu    sync.Mutex
	txCond  *sync.Cond
	nextSeq uint32
	txBase  uint32
	unacked map[uint32]*txSegment

	rxMu     sync.Mutex
	rxCond   *sync.Cond
	recvNext uint32
	ooo      map[uint32]rxSegment
	rxBuf    []byte
	eof      bool

	closeOnce sync.Once
	closedCh  chan struct{}
}

func newARQSession(cfg arqConfig) (*arqSession, error) {
	txAEAD, err := chacha20poly1305.New(cfg.txKey[:])
	if err != nil {
		return nil, err
	}
	rxAEAD, err := chacha20poly1305.New(cfg.rxKey[:])
	if err != nil {
		return nil, err
	}
	baseRTO := 120 * time.Millisecond
	if cfg.initialRTT > 0 {
		if r := cfg.initialRTT * 2; r > baseRTO {
			baseRTO = r
		}
		if baseRTO > 600*time.Millisecond {
			baseRTO = 600 * time.Millisecond
		}
	}
	if cfg.viaRelay {
		// 中继底层已是可靠 TCP/TLS 1.3，放宽 ARQ 超时避免跨洋 RTT 触发无效重传
		if baseRTO < 500*time.Millisecond {
			baseRTO = 500 * time.Millisecond
		}
	}
	s := &arqSession{
		devID:      cfg.devID,
		localID:    cfg.localID,
		remoteID:   cfg.remoteID,
		txAEAD:     txAEAD,
		rxAEAD:     rxAEAD,
		sendUDP:    cfg.sendUDP,
		baseRTO:    baseRTO,
		remoteAddr: cfg.remoteAddr,
		unacked:    map[uint32]*txSegment{},
		ooo:        map[uint32]rxSegment{},
		closedCh:   make(chan struct{}),
	}
	s.lastRxUnix.Store(time.Now().Unix())
	s.txCond = sync.NewCond(&s.txMu)
	s.rxCond = sync.NewCond(&s.rxMu)
	go s.retransmitLoop()
	return s, nil
}

func (s *arqSession) isClosed() bool {
	select {
	case <-s.closedCh:
		return true
	default:
		return false
	}
}

func (s *arqSession) getRemoteAddr() net.Addr {
	s.addrMu.RLock()
	a := s.remoteAddr
	s.addrMu.RUnlock()
	return a
}

func (s *arqSession) setRemoteAddr(a net.Addr) {
	s.addrMu.Lock()
	s.remoteAddr = a
	s.addrMu.Unlock()
}

func (s *arqSession) ackState() (ack uint32, sack uint64) {
	s.rxMu.Lock()
	ack = s.recvNext
	for i := uint32(0); i < 64; i++ {
		if _, exists := s.ooo[ack+1+i]; exists {
			sack |= 1 << i
		}
	}
	s.rxMu.Unlock()
	return ack, sack
}

func (s *arqSession) sendFrame(kind byte, seq uint32, payload []byte) {
	if s.isClosed() {
		return
	}
	ack, sack := s.ackState()
	plain := make([]byte, 17+len(payload))
	plain[0] = kind
	binary.BigEndian.PutUint32(plain[1:5], seq)
	binary.BigEndian.PutUint32(plain[5:9], ack)
	binary.BigEndian.PutUint64(plain[9:17], sack)
	copy(plain[17:], payload)

	pNonce := s.txPktNonce.Add(1)
	var aeadNonce [12]byte
	binary.BigEndian.PutUint64(aeadNonce[4:12], pNonce)

	pkt := make([]byte, 17, 17+len(plain)+16)
	pkt[0] = pktTypeData
	binary.BigEndian.PutUint64(pkt[1:9], s.remoteID)
	binary.BigEndian.PutUint64(pkt[9:17], pNonce)
	pkt = s.txAEAD.Seal(pkt, aeadNonce[:], plain, pkt[:17])

	if to := s.getRemoteAddr(); to != nil {
		s.sendUDP(pkt, to)
	}
}

func (s *arqSession) checkReplay(pNonce uint64) bool {
	if pNonce == 0 {
		return false
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if pNonce > s.maxNonce {
		shift := pNonce - s.maxNonce
		if shift >= 64 {
			s.replayMap = 1
		} else {
			s.replayMap = (s.replayMap << shift) | 1
		}
		s.maxNonce = pNonce
		return true
	}
	diff := s.maxNonce - pNonce
	if diff >= 64 {
		return false
	}
	bit := uint64(1) << diff
	if (s.replayMap & bit) != 0 {
		return false
	}
	s.replayMap |= bit
	return true
}

func (s *arqSession) onEncryptedPacket(pkt []byte, from net.Addr) {
	if len(pkt) < 17+17+16 || s.isClosed() {
		return
	}
	pNonce := binary.BigEndian.Uint64(pkt[9:17])
	var aeadNonce [12]byte
	binary.BigEndian.PutUint64(aeadNonce[4:12], pNonce)

	plain, err := s.rxAEAD.Open(nil, aeadNonce[:], pkt[17:], pkt[:17])
	if err != nil || len(plain) < 17 {
		return
	}
	if !s.checkReplay(pNonce) {
		return
	}
	s.lastRxUnix.Store(time.Now().Unix())
	s.setRemoteAddr(from) // WireGuard 式无缝漫游（Wi-Fi <-> 5G 切网不断流）

	kind := plain[0]
	seq := binary.BigEndian.Uint32(plain[1:5])
	ack := binary.BigEndian.Uint32(plain[5:9])
	sack := binary.BigEndian.Uint64(plain[9:17])
	body := plain[17:]

	// 1. 更新发送端确认窗口
	s.txMu.Lock()
	woke := false
	if ack > s.txBase {
		for id := s.txBase; id < ack; id++ {
			delete(s.unacked, id)
		}
		s.txBase = ack
		woke = true
	}
	for i := uint32(0); i < 64; i++ {
		if (sack & (1 << i)) != 0 {
			if _, ok := s.unacked[ack+1+i]; ok {
				delete(s.unacked, ack+1+i)
				woke = true
			}
		}
	}
	if woke {
		s.txCond.Broadcast()
	}
	s.txMu.Unlock()

	if kind == frameAck {
		return
	}
	if kind == framePing {
		s.sendFrame(frameAck, 0, nil)
		return
	}
	if kind != frameData && kind != frameFin {
		return
	}

	// 2. 写入接收窗口并按序交付给上层 yamux
	s.rxMu.Lock()
	if seq >= s.recvNext && seq < s.recvNext+arqWindowSegs*2 {
		if _, dup := s.ooo[seq]; !dup {
			cp := make([]byte, len(body))
			copy(cp, body)
			s.ooo[seq] = rxSegment{kind: kind, data: cp}
		}
		advanced := false
		for {
			seg, ok := s.ooo[s.recvNext]
			if !ok {
				break
			}
			delete(s.ooo, s.recvNext)
			s.recvNext++
			advanced = true
			if len(seg.data) > 0 {
				s.rxBuf = append(s.rxBuf, seg.data...)
			}
			if seg.kind == frameFin {
				s.eof = true
			}
		}
		if advanced {
			s.rxCond.Broadcast()
		}
	}
	s.rxMu.Unlock()

	s.sendFrame(frameAck, 0, nil)
}

func (s *arqSession) retransmitLoop() {
	t := time.NewTicker(45 * time.Millisecond)
	defer t.Stop()
	ticks := 0
	for {
		select {
		case <-t.C:
			ticks++
			now := time.Now()
			if now.Unix()-s.lastRxUnix.Load() > 50 {
				_ = s.Close()
				return
			}
			var resend []*txSegment
			s.txMu.Lock()
			for seq := s.txBase; seq < s.nextSeq && len(resend) < 32; seq++ {
				seg, ok := s.unacked[seq]
				if !ok {
					continue
				}
				rto := s.baseRTO
				if rto <= 0 {
					rto = 120 * time.Millisecond
				}
				if seg.tries > 2 {
					rto = rto * 2
					if rto > 1500*time.Millisecond {
						rto = 1500 * time.Millisecond
					}
				}
				if now.Sub(seg.sentAt) >= rto {
					seg.sentAt = now
					seg.tries++
					resend = append(resend, seg)
				}
			}
			s.txMu.Unlock()

			for _, seg := range resend {
				s.sendFrame(seg.kind, seg.seq, seg.data)
			}
			if ticks%250 == 0 && len(resend) == 0 {
				s.sendFrame(framePing, 0, nil)
			}
		case <-s.closedCh:
			return
		}
	}
}

func (s *arqSession) Read(p []byte) (int, error) {
	s.rxMu.Lock()
	defer s.rxMu.Unlock()
	for len(s.rxBuf) == 0 {
		if s.eof {
			return 0, io.EOF
		}
		if s.isClosed() {
			return 0, io.EOF
		}
		s.rxCond.Wait()
	}
	n := copy(p, s.rxBuf)
	if n == len(s.rxBuf) {
		s.rxBuf = s.rxBuf[:0]
	} else {
		s.rxBuf = s.rxBuf[n:]
	}
	return n, nil
}

func (s *arqSession) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxChunkPayload {
			chunk = chunk[:maxChunkPayload]
		}
		p = p[len(chunk):]

		cp := make([]byte, len(chunk))
		copy(cp, chunk)

		s.txMu.Lock()
		for !s.isClosed() && s.nextSeq-s.txBase >= arqWindowSegs {
			s.txCond.Wait()
		}
		if s.isClosed() {
			s.txMu.Unlock()
			return total, net.ErrClosed
		}
		seq := s.nextSeq
		s.nextSeq++
		s.unacked[seq] = &txSegment{
			kind:   frameData,
			seq:    seq,
			data:   cp,
			sentAt: time.Now(),
		}
		s.txMu.Unlock()

		s.sendFrame(frameData, seq, cp)
		total += len(chunk)
	}
	return total, nil
}

func (s *arqSession) Close() error {
	s.closeOnce.Do(func() {
		s.txMu.Lock()
		finSeq := s.nextSeq
		s.nextSeq++
		s.txMu.Unlock()
		s.sendFrame(frameFin, finSeq, nil)

		close(s.closedCh)
		s.txCond.Broadcast()
		s.rxCond.Broadcast()
	})
	return nil
}
