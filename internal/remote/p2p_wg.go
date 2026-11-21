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
	PeerIDHex     string
	PeerPSKHex    string
	ServerPubB64  string
	Candidates    []string
	ServerName    string
}

func BuildP2PURI(opts P2PClientOptions) string {
	primary := "127.0.0.1:51820"
	if len(opts.Candidates) > 0 {
		primary = opts.Candidates[0]
	}
	pskToken := opts.PeerPSKHex
	if rawPSK, err := hex.DecodeString(opts.PeerPSKHex); err == nil && len(rawPSK) == 32 {
		pskToken = base64.RawURLEncoding.EncodeToString(rawPSK)
	}

	rawQuery := "pub=" + opts.ServerPubB64
	if len(opts.Candidates) > 1 {
		var extras []string
		curLen := 96 + len(primary) + len(opts.ServerName)*3
		for _, ep := range opts.Candidates[1:] {
			if curLen+len(ep)+4 > 310 {
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
	if len(cands) == 0 {
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

	mu         sync.RWMutex
	peers      map[[8]byte]P2PPeerCredential
	sessions   map[uint64]*arqSession
	stunWaiter *pendingSTUN
	stunAddr   string

	ctx    context.Context
	cancel context.CancelFunc
}

func startP2PInbound(listen, privB64 string, router func(ctx context.Context, devID string, conn net.Conn, dest TargetAddr)) (*p2pInbound, error) {
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
		peers:      map[[8]byte]P2PPeerCredential{},
		sessions:   map[uint64]*arqSession{},
		ctx:        ctx,
		cancel:     cancel,
	}
	go inb.readLoop()
	go inb.stunKeepaliveLoop()
	return inb, nil
}

func (p *p2pInbound) Port() int { return p.port }

func (p *p2pInbound) Close() error {
	p.cancel()
	p.mu.Lock()
	for _, s := range p.sessions {
		_ = s.Close()
	}
	p.sessions = map[uint64]*arqSession{}
	p.mu.Unlock()
	return p.conn.Close()
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

func (p *p2pInbound) handleInit(pkt []byte, addr *net.UDPAddr) {
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

	c2sKey, s2cKey := deriveDirectionalKeys(staticShared, ephShared, cred.PeerPSK[:], clientSessID, serverSessID)
	sess, err := newARQSession(arqConfig{
		devID:      cred.DevID,
		localID:    serverSessID,
		remoteID:   clientSessID,
		txKey:      s2cKey,
		rxKey:      c2sKey,
		sendUDP:    func(b []byte, to net.Addr) { _, _ = p.conn.WriteTo(b, to) },
		remoteAddr: addr,
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
	_, _ = p.conn.WriteToUDP(resp, addr)
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

	udpConn, arq, mux, err := punchAndConnectP2P(ctx, c.opts)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = mux.Close()
		_ = arq.Close()
		_ = udpConn.Close()
		return nil, net.ErrClosed
	}
	if c.mux != nil && !c.mux.IsClosed() {
		_ = mux.Close()
		_ = arq.Close()
		_ = udpConn.Close()
		return c.mux, nil
	}
	c.udpConn = udpConn
	c.arq = arq
	c.mux = mux
	return mux, nil
}

func punchAndConnectP2P(ctx context.Context, opts P2PClientOptions) (*net.UDPConn, *arqSession, *yamux.Session, error) {
	var peerID [8]byte
	var peerPSK [32]byte
	if _, err := hex.Decode(peerID[:], []byte(opts.PeerIDHex)); err != nil {
		return nil, nil, nil, err
	}
	if _, err := hex.Decode(peerPSK[:], []byte(opts.PeerPSKHex)); err != nil {
		return nil, nil, nil, err
	}
	sPubBytes, err := decodeBase64Flexible(opts.ServerPubB64)
	if err != nil || len(sPubBytes) != 32 {
		return nil, nil, nil, errors.New("P2P 服务端公钥无效")
	}
	sStaticPub, err := ecdh.X25519().NewPublicKey(sPubBytes)
	if err != nil {
		return nil, nil, nil, err
	}

	var resolved []*net.UDPAddr
	for _, ep := range opts.Candidates {
		if ua, err := net.ResolveUDPAddr("udp", ep); err == nil {
			resolved = append(resolved, ua)
		}
	}
	if len(resolved) == 0 {
		return nil, nil, nil, errors.New("无法解析 P2P 候选地址")
	}

	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, nil, nil, err
	}
	_ = udpConn.SetReadBuffer(2 << 20)
	_ = udpConn.SetWriteBuffer(2 << 20)

	cEphPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		_ = udpConn.Close()
		return nil, nil, nil, err
	}
	staticShared, err := cEphPriv.ECDH(sStaticPub)
	if err != nil {
		_ = udpConn.Close()
		return nil, nil, nil, err
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

	// 并发向所有候选端点（公网 IPv6 / STUN IPv4 / 局域网 IPv4）发送 UDP 打洞握手包
	sendBurst := func() {
		for _, ua := range resolved {
			_, _ = udpConn.WriteToUDP(initPkt, ua)
		}
	}
	sendBurst()

	hctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	stopPunch := make(chan struct{})
	go func() {
		t := time.NewTicker(220 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				sendBurst()
			case <-stopPunch:
				return
			case <-hctx.Done():
				_ = udpConn.SetReadDeadline(time.Now())
				return
			}
		}
	}()

	buf := make([]byte, 2048)
	var sess *arqSession
	for sess == nil {
		if hctx.Err() != nil {
			close(stopPunch)
			_ = udpConn.Close()
			return nil, nil, nil, errors.New("P2P 打洞连接超时")
		}
		_ = udpConn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, raddr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			close(stopPunch)
			_ = udpConn.Close()
			return nil, nil, nil, err
		}
		pkt := buf[:n]
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

		c2sKey, s2cKey := deriveDirectionalKeys(staticShared, ephShared, peerPSK[:], clientSessID, serverSessID)
		sess, err = newARQSession(arqConfig{
			localID:    clientSessID,
			remoteID:   serverSessID,
			txKey:      c2sKey,
			rxKey:      s2cKey,
			sendUDP:    func(b []byte, to net.Addr) { _, _ = udpConn.WriteTo(b, to) },
			remoteAddr: raddr,
		})
		if err != nil {
			close(stopPunch)
			_ = udpConn.Close()
			return nil, nil, nil, err
		}
	}
	close(stopPunch)
	_ = udpConn.SetReadDeadline(time.Time{})

	// 后台收包循环
	go func() {
		rb := make([]byte, 2048)
		for {
			n, raddr, err := udpConn.ReadFromUDP(rb)
			if err != nil {
				_ = sess.Close()
				return
			}
			if n >= 25 && rb[0] == pktTypeData && binary.BigEndian.Uint64(rb[1:9]) == clientSessID {
				sess.onEncryptedPacket(rb[:n], raddr)
			}
		}
	}()

	mcfg := yamux.DefaultConfig()
	mcfg.EnableKeepAlive = true
	mcfg.KeepAliveInterval = 20 * time.Second
	mcfg.LogOutput = io.Discard
	mux, err := yamux.Client(sess, mcfg)
	if err != nil {
		_ = sess.Close()
		_ = udpConn.Close()
		return nil, nil, nil, err
	}
	return udpConn, sess, mux, nil
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
	s := &arqSession{
		devID:      cfg.devID,
		localID:    cfg.localID,
		remoteID:   cfg.remoteID,
		txAEAD:     txAEAD,
		rxAEAD:     rxAEAD,
		sendUDP:    cfg.sendUDP,
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
				rto := 85 * time.Millisecond
				if seg.tries > 2 {
					rto = 220 * time.Millisecond
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
