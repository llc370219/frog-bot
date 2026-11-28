package remote

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// e2eeInbound bridges BOTH:
//  1. An outbound standard WSS (443) connection to a zero-knowledge Blind Relay
//     (free Cloudflare Workers Durable Object or self-hosted VPS `/api/v2/relay/ws`)
//  2. An optional LAN-only authenticated E2EE WebSocket listener (e.g. :8089) so
//     phones on the same home Wi-Fi connect directly at LAN speed while the main
//     Windows/Mac web UI remains safely locked to 127.0.0.1!
type e2eeInbound struct {
	hostID     string
	staticPriv *ecdh.PrivateKey
	staticPub  string
	relayURL   string
	relayToken string
	lanListen  string
	handler    http.Handler
	dialer     *websocket.Dialer

	mu             sync.RWMutex
	peers          map[string]P2PPeerCredential // peerIDHex -> cred
	relayConnected bool
	relayErr       string
	lanPort        int
	streams        map[string]context.CancelFunc

	ctx    context.Context
	cancel context.CancelFunc
	lanSrv *http.Server
	wg     sync.WaitGroup
}

func startE2EEInbound(privB64, pubB64, relayURL, relayToken, lanListen string, handler http.Handler, dialer *websocket.Dialer) (*e2eeInbound, error) {
	privRaw, err := decodeBase64Flexible(privB64)
	if err != nil || len(privRaw) != 32 {
		return nil, errors.New("无效的 E2EE 私钥")
	}
	priv, err := ecdh.X25519().NewPrivateKey(privRaw)
	if err != nil {
		return nil, err
	}
	hostID := HostIDFromP2PPubKey(pubB64)
	if hostID == "" {
		return nil, errors.New("无效的 E2EE 公钥")
	}
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	ctx, cancel := context.WithCancel(context.Background())
	inb := &e2eeInbound{
		hostID:     hostID,
		staticPriv: priv,
		staticPub:  pubB64,
		relayURL:   strings.TrimSpace(relayURL),
		relayToken: strings.TrimSpace(relayToken),
		lanListen:  strings.TrimSpace(lanListen),
		handler:    handler,
		dialer:     dialer,
		peers:      make(map[string]P2PPeerCredential),
		streams:    make(map[string]context.CancelFunc),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Optional dedicated LAN E2EE WebSocket listener (accepts ONLY X25519+PSK E2EE envelopes)
	if inb.lanListen != "" && !strings.EqualFold(inb.lanListen, "off") {
		ln, err := net.Listen("tcp", inb.lanListen)
		if err == nil {
			if ta, ok := ln.Addr().(*net.TCPAddr); ok {
				inb.lanPort = ta.Port
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/ws", inb.serveDirectLANWS)
			inb.lanSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			inb.wg.Add(1)
			go func() {
				defer inb.wg.Done()
				_ = inb.lanSrv.Serve(ln)
			}()
		}
	}

	if inb.relayURL != "" {
		inb.wg.Add(1)
		go inb.relayLoop()
	}
	return inb, nil
}

func (e *e2eeInbound) UpdatePeers(creds []P2PPeerCredential) {
	m := make(map[string]P2PPeerCredential, len(creds))
	for _, c := range creds {
		m[hex.EncodeToString(c.PeerID[:])] = c
	}
	e.mu.Lock()
	e.peers = m
	e.mu.Unlock()
}

func (e *e2eeInbound) Status() (hasRelay, relayOK bool, relayErr string, lanPort int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.relayURL != "", e.relayConnected, e.relayErr, e.lanPort
}

func (e *e2eeInbound) Close() error {
	e.cancel()
	if e.lanSrv != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = e.lanSrv.Shutdown(sctx)
		cancel()
	}
	e.mu.Lock()
	for _, c := range e.streams {
		c()
	}
	e.streams = map[string]context.CancelFunc{}
	e.mu.Unlock()
	e.wg.Wait()
	return nil
}

func (e *e2eeInbound) buildRelayDialURL() (string, error) {
	raw := strings.TrimSpace(e.relayURL)
	if strings.HasPrefix(raw, "https://") {
		raw = "wss://" + strings.TrimPrefix(raw, "https://")
	} else if strings.HasPrefix(raw, "http://") {
		raw = "ws://" + strings.TrimPrefix(raw, "http://")
	} else if !strings.Contains(raw, "://") {
		raw = "wss://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws"
	}
	q := u.Query()
	q.Set("role", "host")
	q.Set("room", e.hostID)
	if e.relayToken != "" {
		q.Set("token", e.relayToken)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (e *e2eeInbound) relayLoop() {
	defer e.wg.Done()
	backoff := time.Second
	for e.ctx.Err() == nil {
		dialURL, err := e.buildRelayDialURL()
		if err != nil {
			e.setRelayState(false, "中继地址无效: "+err.Error())
			return
		}
		dctx, dcancel := context.WithTimeout(e.ctx, 12*time.Second)
		ws, _, err := e.dialer.DialContext(dctx, dialURL, nil)
		dcancel()
		if err != nil {
			e.setRelayState(false, err.Error())
			select {
			case <-time.After(backoff):
				if backoff < 15*time.Second {
					backoff *= 2
				}
			case <-e.ctx.Done():
				return
			}
			continue
		}

		backoff = time.Second
		e.setRelayState(true, "")
		e.serveWSLoop(ws, true)
		_ = ws.Close()
		e.setRelayState(false, "连接断开，正在重连")
	}
}

func (e *e2eeInbound) setRelayState(ok bool, errMsg string) {
	e.mu.Lock()
	e.relayConnected = ok
	e.relayErr = errMsg
	e.mu.Unlock()
}

func (e *e2eeInbound) serveDirectLANWS(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{
		ReadBufferSize:  64 << 10,
		WriteBufferSize: 64 << 10,
		CheckOrigin:     func(*http.Request) bool { return true },
	}
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	e.serveWSLoop(ws, false)
}

func (e *e2eeInbound) serveWSLoop(ws *websocket.Conn, sendKeepalive bool) {
	ws.SetReadLimit(16 << 20)
	var wmu sync.Mutex
	writeText := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return ws.WriteMessage(websocket.TextMessage, b)
	}

	connCtx, connCancel := context.WithCancel(e.ctx)
	defer connCancel()
	go func() {
		<-connCtx.Done()
		_ = ws.Close()
	}()

	if sendKeepalive {
		go func() {
			tk := time.NewTicker(25 * time.Second)
			defer tk.Stop()
			for {
				select {
				case <-connCtx.Done():
					return
				case <-tk.C:
					// Plain "ping" matches Cloudflare DO WebSocket Auto-Response (0 hibernation wake cost!)
					if err := writeText([]byte("ping")); err != nil {
						connCancel()
						return
					}
				}
			}
		}()
	}

	for connCtx.Err() == nil {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		if strings.TrimSpace(string(data)) == "pong" || strings.TrimSpace(string(data)) == "ping" {
			if strings.TrimSpace(string(data)) == "ping" {
				_ = writeText([]byte("pong"))
			}
			continue
		}
		var env E2EEEnvelope
		if json.Unmarshal(data, &env) != nil || env.Ciphertext == "" {
			continue
		}
		peerID, ok := strings.CutPrefix(env.From, "client:")
		if !ok {
			continue
		}
		e.mu.RLock()
		cred, exists := e.peers[peerID]
		e.mu.RUnlock()
		if !exists {
			continue
		}
		ephBytes, err := decodeBase64Flexible(env.EphPub)
		if err != nil || len(ephBytes) != 32 {
			continue
		}
		sessionKey, err := DeriveE2EESessionKey(e.staticPriv, ephBytes, cred.PeerPSK[:], e.hostID, peerID)
		if err != nil {
			continue
		}
		plain, err := OpenE2EE(sessionKey, env)
		if err != nil {
			continue
		}
		var inner E2EEInnerFrame
		if json.Unmarshal(plain, &inner) != nil {
			continue
		}

		replyEncrypted := func(fr E2EEInnerFrame) error {
			raw, err := json.Marshal(fr)
			if err != nil {
				return err
			}
			outEnv, err := SealE2EE(sessionKey, "host", "client:"+peerID, env.EphPub, raw)
			if err != nil {
				return err
			}
			wire, err := json.Marshal(outEnv)
			if err != nil {
				return err
			}
			return writeText(wire)
		}

		switch inner.Type {
		case "ping":
			_ = replyEncrypted(E2EEInnerFrame{Type: "pong"})
		case "cancel":
			e.cancelStream(peerID + ":" + inner.Stream)
		case "req":
			go e.handleHTTPRequest(connCtx, cred.DevID, peerID, inner, replyEncrypted)
		}
	}
}

func (e *e2eeInbound) cancelStream(key string) {
	e.mu.Lock()
	c := e.streams[key]
	delete(e.streams, key)
	e.mu.Unlock()
	if c != nil {
		c()
	}
}

func (e *e2eeInbound) handleHTTPRequest(connCtx context.Context, devID, peerID string, reqFrame E2EEInnerFrame, send func(E2EEInnerFrame) error) {
	streamKey := peerID + ":" + reqFrame.Stream
	rctx, cancel := context.WithCancel(connCtx)
	e.mu.Lock()
	e.streams[streamKey] = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.streams, streamKey)
		e.mu.Unlock()
	}()

	var bodyReader io.Reader
	if reqFrame.BodyB64 != "" {
		rawBody, err := base64.StdEncoding.DecodeString(reqFrame.BodyB64)
		if err != nil {
			rawBody, _ = decodeBase64Flexible(reqFrame.BodyB64)
		}
		bodyReader = bytes.NewReader(rawBody)
	}
	method := reqFrame.Method
	if method == "" {
		method = http.MethodGet
	}
	path := reqFrame.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	httpReq, err := http.NewRequestWithContext(context.WithValue(rctx, devIDCtxKey{}, devID), method, "http://frog.internal"+path, bodyReader)
	if err != nil {
		_ = send(E2EEInnerFrame{Type: "resp", Stream: reqFrame.Stream, Status: 400, Error: err.Error(), EOF: true})
		return
	}
	for k, v := range reqFrame.Headers {
		httpReq.Header.Set(k, v)
	}

	rw := &e2eeResponseWriter{
		streamID: reqFrame.Stream,
		hdr:      make(http.Header),
		send:     send,
		cancel:   cancel,
	}
	e.handler.ServeHTTP(rw, httpReq)
	rw.finish()
}

// e2eeMaxChunkBytes caps each encrypted inner frame payload so:
//  1. SSE history replays coalesce hundreds of tiny writes into a handful of 48 KiB frames
//     instead of flooding relays with thousands of 10-byte frames.
//  2. Huge responses (large Coding snapshots, file downloads) never exceed the 16 MiB WS frame limit
//     after base64 + AES-GCM + JSON envelope expansion.
const e2eeMaxChunkBytes = 48 << 10

// e2eeResponseWriter implements http.ResponseWriter + http.Flusher so both standard JSON
// responses and live SSE streams (coalesced on Flush / 48 KiB boundaries) work seamlessly.
type e2eeResponseWriter struct {
	streamID    string
	hdr         http.Header
	status      int
	wroteHeader bool
	streaming   bool
	buf         bytes.Buffer
	send        func(E2EEInnerFrame) error
	cancel      context.CancelFunc
}

func (w *e2eeResponseWriter) Header() http.Header { return w.hdr }

func (w *e2eeResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = code
	ct := strings.ToLower(w.hdr.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		w.streaming = true
		if err := w.send(E2EEInnerFrame{
			Type:    "resp",
			Stream:  w.streamID,
			Status:  w.status,
			Headers: flattenHeaders(w.hdr),
			EOF:     false,
		}); err != nil {
			w.cancel()
		}
	}
}

func (w *e2eeResponseWriter) flushChunks() error {
	if w.buf.Len() == 0 {
		return nil
	}
	raw := w.buf.Bytes()
	for len(raw) > 0 {
		n := len(raw)
		if n > e2eeMaxChunkBytes {
			n = e2eeMaxChunkBytes
		}
		if err := w.send(E2EEInnerFrame{
			Type:    "chunk",
			Stream:  w.streamID,
			BodyB64: base64.StdEncoding.EncodeToString(raw[:n]),
		}); err != nil {
			w.buf.Reset()
			w.cancel()
			return err
		}
		raw = raw[n:]
	}
	w.buf.Reset()
	return nil
}

func (w *e2eeResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if len(p) == 0 {
		return 0, nil
	}
	n, err := w.buf.Write(p)
	if err != nil {
		return n, err
	}
	if w.streaming && w.buf.Len() >= e2eeMaxChunkBytes {
		if err := w.flushChunks(); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (w *e2eeResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.streaming {
		if w.buf.Len() == 0 {
			return
		}
		// Explicit Flush on a non-SSE streaming response -> promote to chunked streaming
		w.streaming = true
		if err := w.send(E2EEInnerFrame{
			Type:    "resp",
			Stream:  w.streamID,
			Status:  w.status,
			Headers: flattenHeaders(w.hdr),
			EOF:     false,
		}); err != nil {
			w.buf.Reset()
			w.cancel()
			return
		}
	}
	_ = w.flushChunks()
}

func (w *e2eeResponseWriter) finish() {
	if !w.wroteHeader {
		w.status = http.StatusOK
	}
	if w.streaming {
		_ = w.flushChunks()
		_ = w.send(E2EEInnerFrame{Type: "end", Stream: w.streamID, EOF: true})
		return
	}
	if w.buf.Len() <= e2eeMaxChunkBytes {
		_ = w.send(E2EEInnerFrame{
			Type:    "resp",
			Stream:  w.streamID,
			Status:  w.status,
			Headers: flattenHeaders(w.hdr),
			BodyB64: base64.StdEncoding.EncodeToString(w.buf.Bytes()),
			EOF:     true,
		})
		w.buf.Reset()
		return
	}
	// Large non-SSE response (e.g. file download / big JSON): stream in safe 48 KiB chunks.
	if err := w.send(E2EEInnerFrame{
		Type:    "resp",
		Stream:  w.streamID,
		Status:  w.status,
		Headers: flattenHeaders(w.hdr),
		EOF:     false,
	}); err != nil {
		w.buf.Reset()
		return
	}
	if err := w.flushChunks(); err != nil {
		return
	}
	_ = w.send(E2EEInnerFrame{Type: "end", Stream: w.streamID, EOF: true})
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vals := range h {
		if len(vals) > 0 {
			out[k] = strings.Join(vals, ", ")
		}
	}
	return out
}

// ---------- Go Reference E2EE Client (for testing & Go CLI/desktop clients) ----------

type E2EEGoClient struct {
	opts       E2EEClientOptions
	ws         *websocket.Conn
	wmu        sync.Mutex
	ephPubB64  string
	sessionKey []byte

	mu      sync.Mutex
	streams map[string]chan E2EEInnerFrame
	closed  chan struct{}
}

func DialE2EEClient(ctx context.Context, rawURI string, dialer *websocket.Dialer) (*E2EEGoClient, error) {
	opts, err := ParseE2EEURI(rawURI)
	if err != nil {
		return nil, err
	}
	ephPriv, err := ecdh.X25519().GenerateKey(randReader)
	if err != nil {
		return nil, err
	}
	hostPub, err := decodeBase64Flexible(opts.ServerPubB64)
	if err != nil {
		return nil, err
	}
	psk, err := hex.DecodeString(opts.PeerPSKHex)
	if err != nil {
		return nil, err
	}
	key, err := DeriveE2EESessionKey(ephPriv, hostPub, psk, opts.HostID, opts.PeerIDHex)
	if err != nil {
		return nil, err
	}
	ephPubB64 := base64.RawURLEncoding.EncodeToString(ephPriv.PublicKey().Bytes())

	targetURL := opts.RelayURL
	if targetURL == "" {
		targetURL = "ws://" + opts.LANAddr + "/ws"
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws"
	}
	q := u.Query()
	q.Set("role", "client")
	q.Set("room", opts.HostID)
	q.Set("peer", opts.PeerIDHex)
	if opts.RelayToken != "" {
		q.Set("token", opts.RelayToken)
	}
	u.RawQuery = q.Encode()

	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	ws, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c := &E2EEGoClient{
		opts:       opts,
		ws:         ws,
		ephPubB64:  ephPubB64,
		sessionKey: key,
		streams:    make(map[string]chan E2EEInnerFrame),
		closed:     make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *E2EEGoClient) Close() error {
	return c.ws.Close()
}

func (c *E2EEGoClient) readLoop() {
	defer close(c.closed)
	for {
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		var env E2EEEnvelope
		if json.Unmarshal(data, &env) != nil || env.Ciphertext == "" {
			continue
		}
		pt, err := OpenE2EE(c.sessionKey, env)
		if err != nil {
			continue
		}
		var fr E2EEInnerFrame
		if json.Unmarshal(pt, &fr) != nil {
			continue
		}
		c.mu.Lock()
		ch := c.streams[fr.Stream]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- fr:
			default:
			}
		}
	}
}

func (c *E2EEGoClient) sendInner(fr E2EEInnerFrame) error {
	raw, err := json.Marshal(fr)
	if err != nil {
		return err
	}
	env, err := SealE2EE(c.sessionKey, "client:"+c.opts.PeerIDHex, "host", c.ephPubB64, raw)
	if err != nil {
		return err
	}
	wire, err := json.Marshal(env)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, wire)
}

func (c *E2EEGoClient) DoRequest(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	sid := randomHex(6)
	ch := make(chan E2EEInnerFrame, 8)
	c.mu.Lock()
	c.streams[sid] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.streams, sid)
		c.mu.Unlock()
	}()

	fr := E2EEInnerFrame{Type: "req", Stream: sid, Method: method, Path: path}
	if len(body) > 0 {
		fr.BodyB64 = base64.StdEncoding.EncodeToString(body)
	}
	if err := c.sendInner(fr); err != nil {
		return 0, nil, err
	}

	var status int
	var out bytes.Buffer
	for {
		select {
		case resp := <-ch:
			if resp.Status > 0 {
				status = resp.Status
			}
			if resp.BodyB64 != "" {
				b, _ := base64.StdEncoding.DecodeString(resp.BodyB64)
				out.Write(b)
			}
			if resp.EOF || resp.Type == "end" {
				return status, out.Bytes(), nil
			}
		case <-c.closed:
			return 0, nil, errors.New("E2EE tunnel closed")
		case <-ctx.Done():
			_ = c.sendInner(E2EEInnerFrame{Type: "cancel", Stream: sid})
			return 0, nil, ctx.Err()
		}
	}
}

var randReader = newCryptoRandReader()

type cryptoRandReader struct{}

func newCryptoRandReader() io.Reader { return cryptoRandReader{} }
func (cryptoRandReader) Read(p []byte) (int, error) {
	hexStr := randomHex(len(p))
	b, _ := hex.DecodeString(hexStr)
	copy(p, b)
	return len(p), nil
}
