package remote

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	FrogInternalHost = "frog.internal"
	FrogVirtualIP    = "10.247.0.1"
)

type TargetAddr struct {
	Host string
	Port uint16
}

func (a TargetAddr) String() string {
	return net.JoinHostPort(a.Host, strconv.Itoa(int(a.Port)))
}

func ParseTargetAddr(addr string) TargetAddr {
	h, pStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return TargetAddr{Host: strings.Trim(addr, "[]"), Port: 80}
	}
	p, _ := strconv.Atoi(pStr)
	if p < 0 || p > 65535 {
		p = 80
	}
	return TargetAddr{Host: strings.Trim(h, "[]"), Port: uint16(p)}
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func isFrogInternalHost(hostOrHostPort string) bool {
	h := strings.TrimSpace(hostOrHostPort)
	if sh, _, err := net.SplitHostPort(h); err == nil {
		h = sh
	}
	h = strings.Trim(h, "[]")
	return strings.EqualFold(h, FrogInternalHost) || h == FrogVirtualIP || h == "10.255.255.1"
}

// isInternalTarget 仅允许隧道访问 frog 自身及主机所在内网/回环地址，防止配对码泄露后演变为公网开放跳板。
func isInternalTarget(hostOrHostPort string) bool {
	if isFrogInternalHost(hostOrHostPort) {
		return true
	}
	h := strings.TrimSpace(hostOrHostPort)
	if sh, _, err := net.SplitHostPort(h); err == nil {
		h = sh
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") ||
		strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".home.arpa") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	// Carrier-Grade NAT / Tailscale / WireGuard 常见内网网段 100.64.0.0/10
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && (ip4[1]&0xc0) == 64 {
		return true
	}
	return false
}

func detectAdvertiseIP() string {
	conn, err := net.DialTimeout("udp4", "1.1.1.1:80", 500*time.Millisecond)
	if err == nil {
		defer conn.Close()
		if udp, ok := conn.LocalAddr().(*net.UDPAddr); ok && udp.IP != nil && !udp.IP.IsLoopback() {
			return udp.IP.String()
		}
	}
	conn6, err := net.DialTimeout("udp6", "[2606:4700:4700::1111]:80", 500*time.Millisecond)
	if err == nil {
		defer conn6.Close()
		if udp, ok := conn6.LocalAddr().(*net.UDPAddr); ok && udp.IP != nil && !udp.IP.IsLoopback() && !udp.IP.IsLinkLocalUnicast() {
			return udp.IP.String()
		}
	}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return "127.0.0.1"
}

// discoverLocalEndpoints 收集本机公网 IPv6 与局域网 IPv4 候选端点（供 P2P 直连打洞并发竞速）。
func discoverLocalEndpoints(port int) []string {
	if port <= 0 {
		return nil
	}
	portStr := strconv.Itoa(port)
	var v6List, v4List []string
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				ep := net.JoinHostPort(ip4.String(), portStr)
				if !slices.Contains(v4List, ep) {
					v4List = append(v4List, ep)
				}
			} else if ipnet.IP.To16() != nil && !ipnet.IP.IsPrivate() {
				ep := net.JoinHostPort(ipnet.IP.String(), portStr)
				if !slices.Contains(v6List, ep) {
					v6List = append(v6List, ep)
				}
			}
		}
	}
	// 公网 IPv6 优先于内网 IPv4，最多保留 6 个候选地址以控制二维码密度
	out := append(v6List, v4List...)
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

func normalizeListenAddr(raw, defaultAddr string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return defaultAddr
	}
	if !strings.Contains(s, ":") {
		return ":" + s
	}
	return s
}

func normalizeRelayAddr(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	return net.JoinHostPort(strings.Trim(s, "[]"), "18443")
}

func cleanHostInput(raw string) string {
	h := strings.TrimSpace(raw)
	if h == "" {
		return ""
	}
	if strings.Contains(h, "://") {
		if idx := strings.Index(h, "://"); idx >= 0 {
			h = h[idx+3:]
		}
	}
	if idx := strings.IndexByte(h, '/'); idx >= 0 {
		h = h[:idx]
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		return strings.Trim(h, "[]")
	}
	if sh, _, err := net.SplitHostPort(h); err == nil && sh != "" {
		return strings.Trim(sh, "[]")
	}
	return h
}

func pipeBidirectional(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		_ = b.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		_ = a.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	}()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

type taggedConn struct {
	net.Conn
	devID  string
	closed chan struct{}
	once   sync.Once
}

func (c *taggedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		if c.closed != nil {
			close(c.closed)
		}
	})
	return err
}

type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{
		ch:   make(chan net.Conn, 64),
		done: make(chan struct{}),
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Push(c net.Conn) bool {
	select {
	case <-l.done:
		_ = c.Close()
		return false
	default:
	}
	select {
	case l.ch <- c:
		return true
	case <-l.done:
		_ = c.Close()
		return false
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80}
}
