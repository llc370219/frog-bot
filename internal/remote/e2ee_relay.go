package remote

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSSRelayLimits configures anti-abuse protection for a public WSS blind relay.
type WSSRelayLimits struct {
	MaxConnsPerIP     int
	MaxRooms          int
	MaxClientsPerRoom int
	MaxMessageBytes   int64
	MaxMsgsPerSec     float64
	BurstMsgs         float64
	MaxBytesPerMin    int64
	IdleTimeout       time.Duration
}

// DefaultWSSRelayLimits returns production-safe defaults for a shared public blind relay.
func DefaultWSSRelayLimits() WSSRelayLimits {
	return WSSRelayLimits{
		MaxConnsPerIP:     64,
		MaxRooms:          10000,
		MaxClientsPerRoom: 16,
		MaxMessageBytes:   16 << 20, // 16 MiB max frame for uploads/screenshots
		MaxMsgsPerSec:     120,
		BurstMsgs:         240,
		MaxBytesPerMin:    64 << 20, // 64 MiB/min per socket (plenty for uploads, stops abuse)
		IdleTimeout:       90 * time.Second,
	}
}

func (l WSSRelayLimits) withDefaults() WSSRelayLimits {
	def := DefaultWSSRelayLimits()
	if l.MaxConnsPerIP <= 0 {
		l.MaxConnsPerIP = def.MaxConnsPerIP
	}
	if l.MaxRooms <= 0 {
		l.MaxRooms = def.MaxRooms
	}
	if l.MaxClientsPerRoom <= 0 {
		l.MaxClientsPerRoom = def.MaxClientsPerRoom
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = def.MaxMessageBytes
	}
	if l.MaxMsgsPerSec <= 0 {
		l.MaxMsgsPerSec = def.MaxMsgsPerSec
	}
	if l.BurstMsgs <= 0 {
		l.BurstMsgs = def.BurstMsgs
	}
	if l.MaxBytesPerMin <= 0 {
		l.MaxBytesPerMin = def.MaxBytesPerMin
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = def.IdleTimeout
	}
	return l
}

// WSSRelayStats exposes anonymous health metrics without leaking room IDs or client IPs.
type WSSRelayStats struct {
	OK        bool   `json:"ok"`
	Service   string `json:"service"`
	Rooms     int    `json:"rooms"`
	Hosts     int    `json:"hosts"`
	Clients   int    `json:"clients"`
	UptimeSec int64  `json:"uptime_sec"`
}

// WSSBlindRelay is a standard HTTPS/WSS (443-compatible) zero-knowledge blind relay
// sharing the EXACT same protocol as `deploy/cf-relay/` (Cloudflare Workers Free Plan).
// It forwards opaque E2EEEnvelope frames between `role=host` and `role=client` sockets
// in the same `room` without ever seeing plaintext.
type WSSBlindRelay struct {
	Token     string // optional shared access token
	Limits    WSSRelayLimits
	startedAt time.Time

	mu      sync.RWMutex
	rooms   map[string]*blindRoom
	ipConns map[string]int
}

type blindConn struct {
	ws  *websocket.Conn
	wmu sync.Mutex
}

func (c *blindConn) writeText(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

type blindRoom struct {
	host    *blindConn
	clients map[string]*blindConn // peerID -> conn
}

type connRateBucket struct {
	tokens    float64
	rate      float64
	burst     float64
	lastCheck time.Time
}

func newConnRateBucket(ratePerSec, burst float64) *connRateBucket {
	return &connRateBucket{
		tokens:    burst,
		rate:      ratePerSec,
		burst:     burst,
		lastCheck: time.Now(),
	}
}

func (b *connRateBucket) allow(cost float64) bool {
	now := time.Now()
	if elapsed := now.Sub(b.lastCheck).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.lastCheck = now
	if b.tokens < cost {
		return false
	}
	b.tokens -= cost
	return true
}

func NewWSSBlindRelay(token string) *WSSBlindRelay {
	return NewWSSBlindRelayWithLimits(token, DefaultWSSRelayLimits())
}

func NewWSSBlindRelayWithLimits(token string, limits WSSRelayLimits) *WSSBlindRelay {
	return &WSSBlindRelay{
		Token:     strings.TrimSpace(token),
		Limits:    limits.withDefaults(),
		startedAt: time.Now(),
		rooms:     make(map[string]*blindRoom),
		ipConns:   make(map[string]int),
	}
}

func (r *WSSBlindRelay) Stats() WSSRelayStats {
	r.mu.RLock()
	rooms := len(r.rooms)
	hosts := 0
	clients := 0
	for _, rm := range r.rooms {
		if rm.host != nil {
			hosts++
		}
		clients += len(rm.clients)
	}
	r.mu.RUnlock()
	return WSSRelayStats{
		OK:        true,
		Service:   "frog-e2ee-relay",
		Rooms:     rooms,
		Hosts:     hosts,
		Clients:   clients,
		UptimeSec: int64(time.Since(r.startedAt).Seconds()),
	}
}

func extractRelayClientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(req.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(req.RemoteAddr)
	}
	ip := net.ParseIP(host)
	// Trust proxy headers only when behind local reverse proxy (Caddy/Nginx on loopback).
	if ip != nil && ip.IsLoopback() {
		if xri := strings.TrimSpace(req.Header.Get("X-Real-IP")); xri != "" {
			return xri
		}
		if xff := strings.TrimSpace(req.Header.Get("X-Forwarded-For")); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if first = strings.TrimSpace(first); first != "" {
				return first
			}
		}
	}
	return host
}

// ServeWS handles GET /ws?role=host|client&room=<host_id>&peer=<peer_id>&token=<token>.
func (r *WSSBlindRelay) ServeWS(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	if r.Token != "" {
		got := strings.TrimSpace(q.Get("token"))
		if got == "" {
			got = strings.TrimSpace(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(r.Token)) != 1 {
			http.Error(w, "unauthorized relay token", http.StatusUnauthorized)
			return
		}
	}

	role := strings.ToLower(strings.TrimSpace(q.Get("role")))
	roomID := strings.ToLower(strings.TrimSpace(q.Get("room")))
	peerID := strings.ToLower(strings.TrimSpace(q.Get("peer")))
	if roomID == "" || len(roomID) > 128 || (role != "host" && role != "client") || (role == "client" && (peerID == "" || len(peerID) > 64)) {
		http.Error(w, "missing role/room/peer", http.StatusBadRequest)
		return
	}

	limits := r.Limits.withDefaults()
	ip := extractRelayClientIP(req)
	if status, msg := r.admit(ip, roomID, role, peerID, limits); status != 0 {
		http.Error(w, msg, status)
		return
	}

	up := websocket.Upgrader{
		ReadBufferSize:  64 << 10,
		WriteBufferSize: 64 << 10,
		CheckOrigin:     func(*http.Request) bool { return true },
	}
	ws, err := up.Upgrade(w, req, nil)
	if err != nil {
		r.releaseIP(ip)
		return
	}
	ws.SetReadLimit(limits.MaxMessageBytes)
	ws.SetPingHandler(func(appData string) error {
		_ = ws.SetReadDeadline(time.Now().Add(limits.IdleTimeout))
		return ws.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})

	bc := &blindConn{ws: ws}
	r.register(roomID, role, peerID, bc)
	defer func() {
		r.unregister(roomID, role, peerID, bc)
		r.releaseIP(ip)
		_ = ws.Close()
	}()

	msgLimiter := newConnRateBucket(limits.MaxMsgsPerSec, limits.BurstMsgs)
	byteLimiter := newConnRateBucket(float64(limits.MaxBytesPerMin)/60.0, float64(limits.MaxBytesPerMin))

	for {
		_ = ws.SetReadDeadline(time.Now().Add(limits.IdleTimeout))
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		if !msgLimiter.allow(1) {
			_ = bc.writeText([]byte(`{"err":"rate_limited"}`))
			continue
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "ping" {
			_ = bc.writeText([]byte("pong"))
			continue
		}
		if trimmed == "pong" {
			continue
		}
		if !byteLimiter.allow(float64(len(data))) {
			_ = bc.writeText([]byte(`{"err":"bandwidth_limited"}`))
			continue
		}
		var env struct {
			To string `json:"to"`
		}
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if role == "client" {
			hostConn := r.getHost(roomID)
			if hostConn == nil {
				_ = bc.writeText([]byte(`{"err":"host_offline"}`))
				continue
			}
			_ = hostConn.writeText(data)
		} else {
			// role == "host" -> forward to specific "client:<peer_id>"
			if targetPeer, ok := strings.CutPrefix(env.To, "client:"); ok {
				if cli := r.getClient(roomID, targetPeer); cli != nil {
					_ = cli.writeText(data)
				}
			}
		}
	}
}

func (r *WSSBlindRelay) admit(ip, roomID, role, peerID string, limits WSSRelayLimits) (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ip != "" && r.ipConns[ip] >= limits.MaxConnsPerIP {
		return http.StatusTooManyRequests, "too many connections from ip"
	}
	rm := r.rooms[roomID]
	if rm == nil && len(r.rooms) >= limits.MaxRooms {
		return http.StatusServiceUnavailable, "relay capacity reached"
	}
	if role == "client" && rm != nil {
		if _, exists := rm.clients[peerID]; !exists && len(rm.clients) >= limits.MaxClientsPerRoom {
			return http.StatusTooManyRequests, "too many clients in room"
		}
	}
	if ip != "" {
		r.ipConns[ip]++
	}
	return 0, ""
}

func (r *WSSBlindRelay) releaseIP(ip string) {
	if ip == "" {
		return
	}
	r.mu.Lock()
	if n := r.ipConns[ip]; n <= 1 {
		delete(r.ipConns, ip)
	} else {
		r.ipConns[ip] = n - 1
	}
	r.mu.Unlock()
}

func (r *WSSBlindRelay) register(roomID, role, peerID string, bc *blindConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rm := r.rooms[roomID]
	if rm == nil {
		rm = &blindRoom{clients: make(map[string]*blindConn)}
		r.rooms[roomID] = rm
	}
	if role == "host" {
		if rm.host != nil && rm.host != bc {
			_ = rm.host.ws.Close()
		}
		rm.host = bc
	} else {
		if old := rm.clients[peerID]; old != nil && old != bc {
			_ = old.ws.Close()
		}
		rm.clients[peerID] = bc
	}
}

func (r *WSSBlindRelay) unregister(roomID, role, peerID string, bc *blindConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rm := r.rooms[roomID]
	if rm == nil {
		return
	}
	if role == "host" {
		if rm.host == bc {
			rm.host = nil
		}
	} else if rm.clients[peerID] == bc {
		delete(rm.clients, peerID)
	}
	if rm.host == nil && len(rm.clients) == 0 {
		delete(r.rooms, roomID)
	}
}

func (r *WSSBlindRelay) getHost(roomID string) *blindConn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if rm := r.rooms[roomID]; rm != nil {
		return rm.host
	}
	return nil
}

func (r *WSSBlindRelay) getClient(roomID, peerID string) *blindConn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if rm := r.rooms[roomID]; rm != nil {
		return rm.clients[peerID]
	}
	return nil
}

// StartWSRelayServer starts a standalone HTTP/WebSocket Blind Relay server (`frogbot relay-ws`).
// Place behind Caddy / Nginx / Cloudflare for standard HTTPS/WSS 443.
func StartWSRelayServer(ctx context.Context, listenAddr, token string) (*http.Server, error) {
	return StartWSRelayServerWithLimits(ctx, listenAddr, token, DefaultWSSRelayLimits())
}

// StartWSRelayServerWithLimits starts the standalone WSS blind relay with custom rate/connection limits.
func StartWSRelayServerWithLimits(ctx context.Context, listenAddr, token string, limits WSSRelayLimits) (*http.Server, error) {
	if strings.TrimSpace(listenAddr) == "" {
		listenAddr = ":18443"
	}
	relay := NewWSSBlindRelayWithLimits(token, limits)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", relay.ServeWS)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, relay.Stats())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Frog Bot E2EE Blind Relay OK. Connect via WebSocket /ws\n"))
	})
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	return srv, nil
}
