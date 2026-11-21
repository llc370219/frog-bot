package remote

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ClientTunnel 在客户端（如 iPhone App 进程内）启动本地 127.0.0.1 混合 HTTP/SOCKS5 加密隧道入口，
// 底层直连服务端 sing-box VLESS-Reality 或 WireGuard-P2P，无需系统级 VPN 权限且不顶掉外部代理软件。
type ClientTunnel struct {
	mode       string
	serverName string
	deviceName string
	port       int
	dialRemote func(ctx context.Context, targetAddr string) (net.Conn, error)
	closeUnder func() error

	mu         sync.Mutex
	ln         net.Listener
	httpTr     *http.Transport
	httpProxy  *httputil.ReverseProxy
	closed     bool
}

func StartClientTunnel(rawURI, deviceName string) (*ClientTunnel, error) {
	trimmed := strings.TrimSpace(rawURI)
	lower := strings.ToLower(trimmed)

	t := &ClientTunnel{
		deviceName: strings.TrimSpace(deviceName),
	}

	switch {
	case strings.Contains(lower, "vless://"):
		opts, err := ParseRealityURI(trimmed)
		if err != nil {
			return nil, err
		}
		t.mode = ModeReality
		t.serverName = opts.ServerName
		t.dialRemote = func(ctx context.Context, targetAddr string) (net.Conn, error) {
			return DialRealityVLESS(ctx, opts, targetAddr)
		}
		t.closeUnder = func() error { return nil }

	case strings.Contains(lower, "frogp2p://"):
		opts, err := ParseP2PURI(trimmed)
		if err != nil {
			return nil, err
		}
		p2p := newP2POutbound(opts)
		t.mode = ModeP2P
		t.serverName = opts.ServerName
		t.dialRemote = p2p.DialContext
		t.closeUnder = p2p.Close

	default:
		return nil, errors.New("未识别到有效的加密隧道连接码（支持 vless:// 与 frogp2p://）")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = t.closeUnder()
		return nil, fmt.Errorf("启动内置隧道监听失败: %w", err)
	}
	t.ln = ln
	t.port = ln.Addr().(*net.TCPAddr).Port

	t.httpTr = &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return t.dialRemote(ctx, addr)
		},
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       75 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    true,
	}
	t.httpProxy = &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			host := req.URL.Host
			if host == "" {
				host = req.Host
			}
			if !strings.Contains(host, ":") {
				host = net.JoinHostPort(host, "80")
			}
			req.URL.Host = host
			req.RequestURI = ""
			if t.deviceName != "" && isFrogInternalHost(host) {
				req.Header.Set("X-Frog-Device-Name", t.deviceName)
			}
		},
		Transport:     t.httpTr,
		FlushInterval: -1, // 零缓冲实时透传 SSE 流
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "加密隧道连接失败: " + err.Error()})
		},
	}

	go t.acceptLoop(ln)
	return t, nil
}

func (t *ClientTunnel) Port() int          { return t.port }
func (t *ClientTunnel) Mode() string       { return t.mode }
func (t *ClientTunnel) ServerName() string { return t.serverName }
func (t *ClientTunnel) ProxyURL() string   { return fmt.Sprintf("http://127.0.0.1:%d", t.port) }

func (t *ClientTunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	ln := t.ln
	t.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	if t.httpTr != nil {
		t.httpTr.CloseIdleConnections()
	}
	if t.closeUnder != nil {
		_ = t.closeUnder()
	}
	return nil
}

func (t *ClientTunnel) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			t.mu.Lock()
			closed := t.closed
			if !closed {
				// iOS 从后台挂起恢复时若本地监听被内核回收，自动在同一端口重绑
				if rebound, rerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", t.port)); rerr == nil {
					t.ln = rebound
					ln = rebound
					t.mu.Unlock()
					continue
				}
			}
			t.mu.Unlock()
			if closed {
				return
			}
			time.Sleep(150 * time.Millisecond)
			continue
		}
		go t.serveMixedConn(conn)
	}
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (t *ClientTunnel) serveMixedConn(conn net.Conn) {
	br := bufio.NewReaderSize(conn, 8192)
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	first, err := br.Peek(1)
	if err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	if first[0] == 0x05 {
		t.serveSOCKS5(&bufferedConn{Conn: conn, r: br})
		return
	}
	t.serveHTTPProxy(&bufferedConn{Conn: conn, r: br})
}

func (t *ClientTunnel) serveHTTPProxy(conn *bufferedConn) {
	defer conn.Close()
	for {
		req, err := http.ReadRequest(conn.r)
		if err != nil {
			return
		}
		if req.Method == http.MethodConnect {
			target := req.Host
			if !strings.Contains(target, ":") {
				target = net.JoinHostPort(target, "443")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			remote, err := t.dialRemote(ctx, target)
			cancel()
			if err != nil {
				_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
				return
			}
			_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
			pipeBidirectional(conn, remote)
			return
		}

		// 标准 HTTP 代理请求（复用底层 VLESS-Reality / P2P 连接池与即时流式 Flush）
		rw := newHTTPConnResponseWriter(conn)
		t.httpProxy.ServeHTTP(rw, req)
		rw.finish()
		if req.Close || rw.shouldClose {
			return
		}
	}
}

func (t *ClientTunnel) serveSOCKS5(conn net.Conn) {
	defer conn.Close()
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil || hdr[0] != 0x05 {
		return
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	var req [4]byte
	if _, err := io.ReadFull(conn, req[:]); err != nil || req[0] != 0x05 || req[1] != 0x01 {
		_, _ = conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch req[3] {
	case 0x01:
		var ip [4]byte
		if _, err := io.ReadFull(conn, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return
		}
		d := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, d); err != nil {
			return
		}
		host = string(d)
	case 0x04:
		var ip [16]byte
		if _, err := io.ReadFull(conn, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	default:
		return
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBuf[:]))))

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	remote, err := t.dialRemote(ctx, target)
	cancel()
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		_ = remote.Close()
		return
	}
	pipeBidirectional(conn, remote)
}

// ---------- 轻量 HTTP/1.1 分块流式响应写出器（零额外缓冲） ----------

type httpConnResponseWriter struct {
	conn        net.Conn
	header      http.Header
	wroteHeader bool
	chunked     bool
	shouldClose bool
}

func newHTTPConnResponseWriter(conn net.Conn) *httpConnResponseWriter {
	return &httpConnResponseWriter{
		conn:   conn,
		header: make(http.Header),
	}
}

func (w *httpConnResponseWriter) Header() http.Header { return w.header }

func (w *httpConnResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if w.header.Get("Content-Length") == "" && code != http.StatusNoContent && code != http.StatusNotModified {
		w.chunked = true
		w.header.Set("Transfer-Encoding", "chunked")
	}
	if strings.EqualFold(w.header.Get("Connection"), "close") {
		w.shouldClose = true
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "HTTP/1.1 %d %s\r\n", code, http.StatusText(code))
	for k, vals := range w.header {
		for _, v := range vals {
			fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
		}
	}
	sb.WriteString("\r\n")
	_, _ = io.WriteString(w.conn, sb.String())
}

func (w *httpConnResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if w.chunked {
		if _, err := fmt.Fprintf(w.conn, "%x\r\n", len(p)); err != nil {
			w.shouldClose = true
			return 0, err
		}
		if _, err := w.conn.Write(p); err != nil {
			w.shouldClose = true
			return 0, err
		}
		if _, err := io.WriteString(w.conn, "\r\n"); err != nil {
			w.shouldClose = true
			return 0, err
		}
		return len(p), nil
	}
	n, err := w.conn.Write(p)
	if err != nil {
		w.shouldClose = true
	}
	return n, err
}

func (w *httpConnResponseWriter) Flush() {}

func (w *httpConnResponseWriter) finish() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.chunked {
		_, _ = io.WriteString(w.conn, "0\r\n\r\n")
	}
}
