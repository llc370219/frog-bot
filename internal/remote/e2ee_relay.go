package remote

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSSBlindRelay is a standard HTTPS/WSS (443-compatible) zero-knowledge blind relay
// sharing the EXACT same protocol as `deploy/cf-relay/` (Cloudflare Workers Free Plan).
// It forwards opaque E2EEEnvelope frames between `role=host` and `role=client` sockets
// in the same `room` without ever seeing plaintext.
type WSSBlindRelay struct {
	Token string // optional shared access token

	mu    sync.RWMutex
	rooms map[string]*blindRoom
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

func NewWSSBlindRelay(token string) *WSSBlindRelay {
	return &WSSBlindRelay{
		Token: strings.TrimSpace(token),
		rooms: make(map[string]*blindRoom),
	}
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
	if roomID == "" || (role != "host" && role != "client") || (role == "client" && peerID == "") {
		http.Error(w, "missing role/room/peer", http.StatusBadRequest)
		return
	}

	up := websocket.Upgrader{
		ReadBufferSize:  64 << 10,
		WriteBufferSize: 64 << 10,
		CheckOrigin:     func(*http.Request) bool { return true },
	}
	ws, err := up.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(16 << 20) // 16 MiB max frame for uploads/screenshots

	bc := &blindConn{ws: ws}
	r.register(roomID, role, peerID, bc)
	defer func() {
		r.unregister(roomID, role, peerID, bc)
		_ = ws.Close()
	}()

	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "ping" {
			_ = bc.writeText([]byte("pong"))
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
	if strings.TrimSpace(listenAddr) == "" {
		listenAddr = ":18443"
	}
	relay := NewWSSBlindRelay(token)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", relay.ServeWS)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"frog-e2ee-relay"}`))
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
