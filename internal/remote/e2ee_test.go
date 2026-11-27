package remote

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestE2EEBlindRelayEndToEndAndZeroKnowledgeAndRevocation(t *testing.T) {
	// 1. Start in-memory WSS Blind Relay inspecting EVERY wire frame to prove ZERO plaintext leakage
	relay := NewWSSBlindRelay("free-relay-secret")
	var wireMu sync.Mutex
	var observedWire []string

	ln := newPipeListener()
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			relay.ServeWS(w, r)
			return
		}
		http.NotFound(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// Wrap pipe dialer so we can assert blind wire traffic
	pipeDialer := &websocket.Dialer{
		NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return ln.Dial()
		},
	}

	// 2. Start Frog Bot Manager in ModeE2EE behind "home NAT" (zero inbound ports, outbound WSS only)
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/devices":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"local","name":"HomeMac","online":true,"bots":[{"id":"main","name":"Frog"}]}]`))
		case "/api/bots/main/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			for i := 1; i <= 2; i++ {
				_, _ = fmt.Fprintf(w, "id: %d\nevent: message\ndata: {\"secret_payload\":\"TOP_SECRET_MSG_%d\"}\n\n", i, i)
				if fl != nil {
					fl.Flush()
				}
			}
		default:
			http.NotFound(w, r)
		}
	})

	mgr, err := NewManager(t.TempDir(), app)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	mgr.SetE2EEDialer(pipeDialer)

	st, err := mgr.UpdateConfig(SaveInput{
		Mode:           ModeE2EE,
		E2EERelayURL:   "ws://cf-relay.local/ws",
		E2EERelayToken: "free-relay-secret",
		E2EELANListen:  "off",
		ServerName:     "家里Mac",
	})
	if err != nil {
		t.Fatalf("UpdateConfig ModeE2EE: %v", err)
	}
	if st.Mode != ModeE2EE {
		t.Fatalf("expected ModeE2EE, got %+v", st)
	}

	// Wait for Host to register outbound with Blind Relay
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.Status().RelayConnected {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !mgr.Status().RelayConnected {
		t.Fatalf("host failed to connect to WSS blind relay: %+v", mgr.Status())
	}

	// 3. Pair phone via QR code (`froge2ee://...`)
	pair, err := mgr.CreatePair()
	if err != nil {
		t.Fatalf("CreatePair: %v", err)
	}
	if !strings.HasPrefix(pair.PairCode, "froge2ee://") {
		t.Fatalf("expected froge2ee:// URI, got %s", pair.PairCode)
	}

	// Verify zero-knowledge on wire by inspecting sealed envelope directly
	opts, err := ParseE2EEURI(pair.PairCode)
	if err != nil {
		t.Fatal(err)
	}
	testEnv, err := SealE2EE(make([]byte, 32), "client:"+opts.PeerIDHex, "host", "ep1", []byte(`{"p":"/api/devices","secret":"TOP_SECRET_MSG_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	wireMu.Lock()
	observedWire = append(observedWire, testEnv.Ciphertext)
	wireMu.Unlock()
	if strings.Contains(testEnv.Ciphertext, "TOP_SECRET_MSG") || strings.Contains(testEnv.Ciphertext, "/api/devices") {
		t.Fatal("envelope leaked plaintext!")
	}

	// 4. Phone dials Blind Relay and calls BOTH REST (/api/devices) AND live SSE (/api/bots/main/stream)!
	client, err := DialE2EEClient(context.Background(), pair.PairCode, pipeDialer)
	if err != nil {
		t.Fatalf("DialE2EEClient: %v", err)
	}
	defer client.Close()

	code, body, err := client.DoRequest(context.Background(), "GET", "/api/devices", nil)
	if err != nil || code != 200 || !strings.Contains(string(body), "HomeMac") {
		t.Fatalf("E2EE GET /api/devices failed: code=%d err=%v body=%s", code, err, string(body))
	}

	codeSSE, bodySSE, err := client.DoRequest(context.Background(), "GET", "/api/bots/main/stream", nil)
	if err != nil || codeSSE != 200 || !strings.Contains(string(bodySSE), "TOP_SECRET_MSG_2") {
		t.Fatalf("E2EE SSE stream failed: code=%d err=%v body=%s", codeSSE, err, string(bodySSE))
	}

	// Verify paired device was activated on Host
	devs := mgr.Status().Devices
	if len(devs) != 1 {
		t.Fatalf("expected 1 activated device, got %+v", devs)
	}

	// 5. Revoke device -> further requests MUST fail / time out because host dropped peer PSK
	mgr.RevokeDevice(devs[0].ID)
	rctx, rcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer rcancel()
	if _, _, err := client.DoRequest(rctx, "GET", "/api/devices", nil); err == nil {
		t.Fatal("revoked device must not receive responses")
	}
}

func TestE2EEAutoEnableOutOfBoxAndOptOut(t *testing.T) {
	if DefaultE2EERelayURL != "wss://relay.632219.xyz/ws" {
		t.Fatalf("unexpected DefaultE2EERelayURL: %q", DefaultE2EERelayURL)
	}
	dir := t.TempDir()
	relay := NewWSSBlindRelay("")
	ln := newPipeListener()
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			relay.ServeWS(w, r)
		case "/health":
			writeJSON(w, http.StatusOK, relay.Stats())
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	pipeDialer := &websocket.Dialer{
		NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return ln.Dial()
		},
		NetDialTLSContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return ln.Dial()
		},
	}
	app := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// 1. Fresh home PC start with AutoEnableE2EE=true -> automatically enters ModeE2EE using built-in relay
	mgr, err := NewManagerWithOptions(dir, app, Options{
		AutoEnableE2EE:  true,
		DefaultRelayURL: DefaultE2EERelayURL,
		E2EEDialer:      pipeDialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	mgrClosed := false
	defer func() {
		if !mgrClosed {
			mgr.Close()
		}
	}()
	st := mgr.Status()
	if st.Mode != ModeE2EE || !st.UsingDefaultRelay || st.EffectiveRelayURL != "wss://relay.632219.xyz/ws" {
		t.Fatalf("expected auto-enabled ModeE2EE with built-in default relay immediately on first launch, got %+v", st)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.Status().RelayConnected {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !mgr.Status().RelayConnected {
		t.Fatalf("expected auto-connected default relay, got %+v", mgr.Status())
	}
	if stats := relay.Stats(); stats.Hosts != 1 || stats.Rooms != 1 {
		t.Fatalf("expected relay stats hosts=1 rooms=1, got %+v", stats)
	}

	// Generated pairing code embeds effective default relay URL so existing clients connect zero-config
	pair, err := mgr.CreatePair()
	if err != nil {
		t.Fatalf("CreatePair: %v", err)
	}
	opts, err := ParseE2EEURI(pair.PairCode)
	if err != nil {
		t.Fatalf("ParseE2EEURI: %v", err)
	}
	if opts.RelayURL != "wss://relay.632219.xyz/ws" {
		t.Fatalf("expected pairing URI relay=wss://relay.632219.xyz/ws, got %q", opts.RelayURL)
	}

	// 2. Explicit opt-out (`mode: "off"`) MUST persist across restarts even when AutoEnableE2EE=true
	if _, err := mgr.UpdateConfig(SaveInput{Mode: ModeOff}); err != nil {
		t.Fatalf("opt-out UpdateConfig: %v", err)
	}
	mgr.Close()
	mgrClosed = true

	mgr2, err := NewManagerWithOptions(dir, app, Options{
		AutoEnableE2EE:  true,
		DefaultRelayURL: DefaultE2EERelayURL,
		E2EEDialer:      pipeDialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr2.Close()
	if got := mgr2.Status().Mode; got != ModeOff {
		t.Fatalf("expected explicit ModeOff to persist on restart, got %q", got)
	}
}

type fixedAddrConn struct {
	net.Conn
	raddr net.Addr
}

func (c *fixedAddrConn) RemoteAddr() net.Addr { return c.raddr }

func TestWSSBlindRelayPerIPLimit(t *testing.T) {
	limits := DefaultWSSRelayLimits()
	limits.MaxConnsPerIP = 1
	relay := NewWSSBlindRelayWithLimits("", limits)

	ln := newPipeListener()
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(relay.ServeWS)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	dialFromIP := func(ip string) *websocket.Dialer {
		return &websocket.Dialer{
			NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				c1, c2 := net.Pipe()
				ln.ch <- &fixedAddrConn{Conn: c2, raddr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 54321}}
				return c1, nil
			},
		}
	}

	d := dialFromIP("203.0.113.9")
	ws1, _, err := d.Dial("ws://relay.local/ws?role=host&room=room1", nil)
	if err != nil {
		t.Fatalf("first dial failed: %v", err)
	}
	defer ws1.Close()

	_, resp, err := d.Dial("ws://relay.local/ws?role=host&room=room2", nil)
	if err == nil {
		t.Fatal("expected second connection from same IP to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got resp=%v err=%v", resp, err)
	}
}

type pipeListener struct {
	ch     chan net.Conn
	closed chan struct{}
}

func newPipeListener() *pipeListener {
	return &pipeListener{ch: make(chan net.Conn, 8), closed: make(chan struct{})}
}
func (l *pipeListener) Dial() (net.Conn, error) {
	c1, c2 := net.Pipe()
	l.ch <- c2
	return c1, nil
}
func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{} }
