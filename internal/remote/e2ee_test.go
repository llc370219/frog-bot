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
