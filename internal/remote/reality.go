package remote

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	utls "github.com/metacubex/utls"
	"golang.org/x/crypto/hkdf"
)

const DefaultRealitySNI = "www.apple.com"

type RealityClientOptions struct {
	ServerAddr string
	UUID       string
	SNI        string
	PublicKey  string
	ShortID    string
	ServerName string
}

func GenerateRealityKeyPair() (privB64, pubB64 string, err error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

func RealityPublicKeyFromPrivate(privB64 string) (string, error) {
	raw, err := decodeBase64Flexible(privB64)
	if err != nil || len(raw) != 32 {
		return "", errors.New("无效的 REALITY 私钥")
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

func decodeBase64Flexible(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("base64 解码失败")
}

func parseUUID16(raw string) ([16]byte, error) {
	var out [16]byte
	clean := strings.ReplaceAll(strings.TrimSpace(raw), "-", "")
	if len(clean) != 32 {
		return out, errors.New("UUID 长度无效")
	}
	if _, err := hex.Decode(out[:], []byte(clean)); err != nil {
		return out, errors.New("UUID 格式无效")
	}
	return out, nil
}

func BuildRealityURI(opts RealityClientOptions) string {
	sni := strings.TrimSpace(opts.SNI)
	if sni == "" {
		sni = DefaultRealitySNI
	}
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("security", "reality")
	q.Set("sni", sni)
	q.Set("fp", "chrome")
	q.Set("pbk", opts.PublicKey)
	q.Set("sid", opts.ShortID)
	q.Set("type", "tcp")
	u := &url.URL{
		Scheme:   "vless",
		User:     url.User(opts.UUID),
		Host:     opts.ServerAddr,
		RawQuery: q.Encode(),
		Fragment: opts.ServerName,
	}
	return u.String()
}

func ParseRealityURI(raw string) (RealityClientOptions, error) {
	s := strings.TrimSpace(raw)
	if idx := strings.Index(strings.ToLower(s), "vless://"); idx >= 0 {
		s = s[idx:]
		if end := strings.IndexAny(s, " \t\r\n"); end >= 0 {
			s = s[:end]
		}
	} else {
		return RealityClientOptions{}, errors.New("未识别到 vless:// 链接")
	}
	u, err := url.Parse(s)
	if err != nil {
		return RealityClientOptions{}, fmt.Errorf("无效的 vless 链接: %w", err)
	}
	uuid := ""
	if u.User != nil {
		uuid = strings.TrimSpace(u.User.Username())
	}
	if _, err := parseUUID16(uuid); err != nil {
		return RealityClientOptions{}, err
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return RealityClientOptions{}, errors.New("vless 链接缺少端口")
	}
	q := u.Query()
	if !strings.EqualFold(q.Get("security"), "reality") {
		return RealityClientOptions{}, errors.New("仅支持 VLESS-Reality 加密协议")
	}
	pbk := strings.TrimSpace(q.Get("pbk"))
	pubBytes, err := decodeBase64Flexible(pbk)
	if err != nil || len(pubBytes) != 32 {
		return RealityClientOptions{}, errors.New("vless 链接 pbk 公钥无效")
	}
	sid := strings.TrimSpace(q.Get("sid"))
	if len(sid) > 16 || len(sid)%2 != 0 {
		return RealityClientOptions{}, errors.New("vless 链接 sid 无效")
	}
	if _, err := hex.DecodeString(sid); err != nil {
		return RealityClientOptions{}, errors.New("vless 链接 sid 须为十六进制")
	}
	sni := strings.TrimSpace(q.Get("sni"))
	if sni == "" {
		sni = DefaultRealitySNI
	}
	name := strings.TrimSpace(u.Fragment)
	if decoded, err := url.PathUnescape(name); err == nil && decoded != "" {
		name = decoded
	}
	return RealityClientOptions{
		ServerAddr: u.Host,
		UUID:       uuid,
		SNI:        sni,
		PublicKey:  base64.RawURLEncoding.EncodeToString(pubBytes),
		ShortID:    strings.ToLower(sid),
		ServerName: name,
	}, nil
}

// ---------- VLESS 公开标准线协议帧编解码（MIT 纯自研实现，零 GPL 依赖） ----------

const (
	vlessCmdTCP    = 0x01
	vlessAddrIPv4  = 0x01
	vlessAddrHost  = 0x02
	vlessAddrIPv6  = 0x03
)

func encodeVLESSRequestHeader(uuid [16]byte, dst TargetAddr) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(0x00) // Version 0
	buf.Write(uuid[:])
	buf.WriteByte(0x00)        // Addons length = 0
	buf.WriteByte(vlessCmdTCP) // Command = TCP

	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], dst.Port)
	buf.Write(portBytes[:])

	ip := net.ParseIP(dst.Host)
	if ip4 := ip.To4(); ip4 != nil {
		buf.WriteByte(vlessAddrIPv4)
		buf.Write(ip4)
	} else if ip6 := ip.To16(); ip6 != nil {
		buf.WriteByte(vlessAddrIPv6)
		buf.Write(ip6)
	} else {
		if len(dst.Host) == 0 || len(dst.Host) > 255 {
			return nil, errors.New("目标域名长度无效")
		}
		buf.WriteByte(vlessAddrHost)
		buf.WriteByte(byte(len(dst.Host)))
		buf.WriteString(dst.Host)
	}
	return buf.Bytes(), nil
}

func readVLESSRequestHeader(r io.Reader) (uuid [16]byte, cmd byte, dst TargetAddr, err error) {
	var head [18]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return
	}
	if head[0] != 0x00 {
		err = errors.New("不支持的 VLESS 版本")
		return
	}
	copy(uuid[:], head[1:17])
	addonsLen := int(head[17])
	if addonsLen > 0 {
		if _, err = io.CopyN(io.Discard, r, int64(addonsLen)); err != nil {
			return
		}
	}
	var meta [4]byte
	if _, err = io.ReadFull(r, meta[:]); err != nil {
		return
	}
	cmd = meta[0]
	dst.Port = binary.BigEndian.Uint16(meta[1:3])
	switch meta[3] {
	case vlessAddrIPv4:
		var ip [4]byte
		if _, err = io.ReadFull(r, ip[:]); err != nil {
			return
		}
		dst.Host = net.IP(ip[:]).String()
	case vlessAddrHost:
		var l [1]byte
		if _, err = io.ReadFull(r, l[:]); err != nil {
			return
		}
		hb := make([]byte, int(l[0]))
		if _, err = io.ReadFull(r, hb); err != nil {
			return
		}
		dst.Host = string(hb)
	case vlessAddrIPv6:
		var ip [16]byte
		if _, err = io.ReadFull(r, ip[:]); err != nil {
			return
		}
		dst.Host = net.IP(ip[:]).String()
	default:
		err = errors.New("未知的 VLESS 地址类型")
		return
	}
	return
}

// vlessServerConn 在服务端首次写回响应数据时自动附带 2 字节 VLESS 响应头 [0x00, 0x00]。
type vlessServerConn struct {
	net.Conn
	wroteResp bool
	mu        sync.Mutex
}

func (c *vlessServerConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if !c.wroteResp {
		c.wroteResp = true
		frame := make([]byte, 2+len(p))
		copy(frame[2:], p)
		_, err := c.Conn.Write(frame)
		c.mu.Unlock()
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}
	c.mu.Unlock()
	return c.Conn.Write(p)
}

// vlessClientConn 在客户端首次写出负载时合并发送 VLESS 请求头（0-RTT Early Data），首次读取时剥离响应头。
type vlessClientConn struct {
	net.Conn
	reqHdr   []byte
	writeMu  sync.Mutex
	wroteReq bool
	readMu   sync.Mutex
	readResp bool
}

func (c *vlessClientConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	if !c.wroteReq {
		c.wroteReq = true
		frame := make([]byte, len(c.reqHdr)+len(p))
		copy(frame, c.reqHdr)
		copy(frame[len(c.reqHdr):], p)
		c.reqHdr = nil
		_, err := c.Conn.Write(frame)
		c.writeMu.Unlock()
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}
	c.writeMu.Unlock()
	return c.Conn.Write(p)
}

func (c *vlessClientConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	if !c.readResp {
		// 确保请求头已发出
		c.writeMu.Lock()
		if !c.wroteReq {
			c.wroteReq = true
			hdr := c.reqHdr
			c.reqHdr = nil
			_, werr := c.Conn.Write(hdr)
			c.writeMu.Unlock()
			if werr != nil {
				c.readMu.Unlock()
				return 0, werr
			}
		} else {
			c.writeMu.Unlock()
		}

		var respHdr [2]byte
		if _, err := io.ReadFull(c.Conn, respHdr[:]); err != nil {
			c.readMu.Unlock()
			return 0, err
		}
		if addonsLen := int(respHdr[1]); addonsLen > 0 {
			if _, err := io.CopyN(io.Discard, c.Conn, int64(addonsLen)); err != nil {
				c.readMu.Unlock()
				return 0, err
			}
		}
		c.readResp = true
	}
	c.readMu.Unlock()
	return c.Conn.Read(p)
}

// ---------- Server: VLESS + REALITY Inbound ----------

type realityServerParams struct {
	Listen         string
	SNI            string
	CamouflageDest string
	PrivateKeyB64  string
	ShortIDHex     string
	Router         func(ctx context.Context, devID string, conn net.Conn, dest TargetAddr)
}

type realityInbound struct {
	ln         net.Listener
	port       string
	realityCfg *utls.RealityConfig
	router     func(ctx context.Context, devID string, conn net.Conn, dest TargetAddr)

	usersMu sync.RWMutex
	users   map[[16]byte]string // UUID -> devID

	ctx    context.Context
	cancel context.CancelFunc
}

func startRealityInbound(p realityServerParams) (*realityInbound, error) {
	privBytes, err := decodeBase64Flexible(p.PrivateKeyB64)
	if err != nil || len(privBytes) != 32 {
		return nil, errors.New("REALITY 私钥长度无效")
	}
	var sid [8]byte
	if p.ShortIDHex != "" {
		n, err := hex.Decode(sid[:], []byte(p.ShortIDHex))
		if err != nil || n > 8 {
			return nil, errors.New("REALITY ShortID 无效")
		}
	}
	sni := strings.TrimSpace(p.SNI)
	if sni == "" {
		sni = DefaultRealitySNI
	}
	if sh, _, err := net.SplitHostPort(sni); err == nil && sh != "" {
		sni = sh
	}
	dest := strings.TrimSpace(p.CamouflageDest)
	if dest == "" {
		dest = net.JoinHostPort(sni, "443")
	} else if !strings.Contains(dest, ":") {
		dest = net.JoinHostPort(dest, "443")
	}

	ln, err := net.Listen("tcp", normalizeListenAddr(p.Listen, ":8443"))
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败: %w", p.Listen, err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	dialer := &net.Dialer{Timeout: 8 * time.Second}
	rc := &utls.RealityConfig{
		DialContext: dialer.DialContext,
		Type:        "tcp",
		Dest:        dest,
		ServerNames: map[string]bool{sni: true},
		PrivateKey:  privBytes,
		ShortIds:    map[[8]byte]bool{sid: true},
	}
	rc.SessionTicketsDisabled = true

	ctx, cancel := context.WithCancel(context.Background())
	inb := &realityInbound{
		ln:         ln,
		port:       port,
		realityCfg: rc,
		router:     p.Router,
		users:      map[[16]byte]string{},
		ctx:        ctx,
		cancel:     cancel,
	}
	go inb.acceptLoop()
	return inb, nil
}

func (r *realityInbound) UpdateUsers(ids, uuids []string) {
	next := make(map[[16]byte]string, len(ids))
	for i := 0; i < len(ids) && i < len(uuids); i++ {
		if u, err := parseUUID16(uuids[i]); err == nil {
			next[u] = ids[i]
		}
	}
	r.usersMu.Lock()
	r.users = next
	r.usersMu.Unlock()
}

func (r *realityInbound) Port() string { return r.port }

func (r *realityInbound) Close() error {
	r.cancel()
	return r.ln.Close()
}

func (r *realityInbound) acceptLoop() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			continue
		}
		go r.serveConn(conn)
	}
}

func (r *realityInbound) serveConn(rawConn net.Conn) {
	defer rawConn.Close()
	hctx, cancel := context.WithTimeout(r.ctx, 12*time.Second)
	tlsConn, err := utls.RealityServer(hctx, rawConn, r.realityCfg)
	cancel()
	if err != nil {
		return
	}
	defer tlsConn.Close()

	_ = tlsConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	uuid, cmd, dst, err := readVLESSRequestHeader(tlsConn)
	if err != nil || cmd != vlessCmdTCP {
		return
	}
	_ = tlsConn.SetReadDeadline(time.Time{})

	r.usersMu.RLock()
	devID, ok := r.users[uuid]
	r.usersMu.RUnlock()
	if !ok || devID == "" {
		return
	}

	r.router(r.ctx, devID, &vlessServerConn{Conn: tlsConn}, dst)
}

// ---------- Client: VLESS + REALITY Outbound ----------

func DialRealityVLESS(ctx context.Context, opts RealityClientOptions, targetAddr string) (net.Conn, error) {
	pubBytes, err := decodeBase64Flexible(opts.PublicKey)
	if err != nil || len(pubBytes) != 32 {
		return nil, errors.New("无效的 REALITY 公钥")
	}
	uuidBytes, err := parseUUID16(opts.UUID)
	if err != nil {
		return nil, err
	}
	var shortID [8]byte
	if opts.ShortID != "" {
		if _, err := hex.Decode(shortID[:], []byte(opts.ShortID)); err != nil {
			return nil, errors.New("无效的 REALITY ShortID")
		}
	}
	sni := opts.SNI
	if sni == "" {
		sni = DefaultRealitySNI
	}
	if sh, _, err := net.SplitHostPort(sni); err == nil && sh != "" {
		sni = sh
	}

	reqHdr, err := encodeVLESSRequestHeader(uuidBytes, ParseTargetAddr(targetAddr))
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 25 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", opts.ServerAddr)
	if err != nil {
		return nil, err
	}

	tlsConn, err := realityClientHandshake(ctx, rawConn, sni, pubBytes, shortID)
	if err != nil {
		_ = rawConn.Close()
		return nil, err
	}

	return &vlessClientConn{
		Conn:   tlsConn,
		reqHdr: reqHdr,
	}, nil
}

func realityClientHandshake(ctx context.Context, rawConn net.Conn, sni string, serverPubKey []byte, shortID [8]byte) (*utls.UConn, error) {
	var authKey []byte
	verified := false

	uConfig := &utls.Config{
		ServerName:             sni,
		InsecureSkipVerify:     true,
		SessionTicketsDisabled: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("对端未返回证书")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			if pub, ok := leaf.PublicKey.(ed25519.PublicKey); ok && len(authKey) == 32 {
				h := hmac.New(sha512.New, authKey)
				h.Write(pub)
				if bytes.Equal(h.Sum(nil), leaf.Signature) {
					verified = true
					return nil
				}
			}
			return errors.New("REALITY 隐身握手未通过身份验证")
		},
	}

	uConn := utls.UClient(rawConn, uConfig, utls.HelloChrome_Auto)
	if err := uConn.BuildHandshakeState(); err != nil {
		return nil, err
	}
	for _, ext := range uConn.Extensions {
		if ce, ok := ext.(*utls.SupportedCurvesExtension); ok {
			filtered := ce.Curves[:0]
			for _, c := range ce.Curves {
				if c != utls.X25519MLKEM768 {
					filtered = append(filtered, c)
				}
			}
			ce.Curves = filtered
		}
		if ks, ok := ext.(*utls.KeyShareExtension); ok {
			filtered := ks.KeyShares[:0]
			for _, s := range ks.KeyShares {
				if s.Group != utls.X25519MLKEM768 {
					filtered = append(filtered, s)
				}
			}
			ks.KeyShares = filtered
		}
	}
	if err := uConn.BuildHandshakeState(); err != nil {
		return nil, err
	}

	hello := uConn.HandshakeState.Hello
	if len(hello.Raw) < 71 || hello.Raw[38] != 32 {
		return nil, errors.New("非预期的 TLS ClientHello 会话结构")
	}
	hello.SessionId = make([]byte, 32)
	copy(hello.Raw[39:71], hello.SessionId)

	nowUnix := uint32(time.Now().Unix())
	hello.SessionId[0] = 1
	hello.SessionId[1] = 8
	hello.SessionId[2] = 1
	hello.SessionId[3] = 0
	binary.BigEndian.PutUint32(hello.SessionId[4:8], nowUnix)
	copy(hello.SessionId[8:16], shortID[:])

	pubKey, err := ecdh.X25519().NewPublicKey(serverPubKey)
	if err != nil {
		return nil, err
	}
	ksKeys := uConn.HandshakeState.State13.KeyShareKeys
	if ksKeys == nil || ksKeys.Ecdhe == nil {
		return nil, errors.New("缺少 X25519 密钥交换上下文")
	}
	sharedSecret, err := ksKeys.Ecdhe.ECDH(pubKey)
	if err != nil || len(sharedSecret) == 0 {
		return nil, errors.New("REALITY ECDH 密钥协商失败")
	}
	authKey = make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, sharedSecret, hello.Random[:20], []byte("REALITY")), authKey); err != nil {
		return nil, err
	}

	aesBlock, err := aes.NewCipher(authKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(aesBlock)
	if err != nil {
		return nil, err
	}
	gcm.Seal(hello.SessionId[:0], hello.Random[20:32], hello.SessionId[:16], hello.Raw)
	copy(hello.Raw[39:71], hello.SessionId)

	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := uConn.HandshakeContext(hctx); err != nil {
		return nil, err
	}
	if !verified {
		_ = uConn.Close()
		return nil, errors.New("REALITY 证书签名校验失败")
	}
	return uConn, nil
}
