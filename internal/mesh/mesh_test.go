package mesh

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

func TestEd25519PairingE2EEEnvelopeAndNATRelayDispatch(t *testing.T) {
	// Set up 3 nodes:
	//   - sgVPS    (public relay node)
	//   - homeNAS  (NAT home node behind router, connects outbound to sgVPS)
	//   - laptop   (another peer connected to sgVPS)
	sgVPS, err := OpenMesh(t.TempDir(), "sg-vps", "[海外VPS]", "https://sg.example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer sgVPS.Close()

	homeNAS, err := OpenMesh(t.TempDir(), "home-nas", "[家庭宽带IP]", "")
	if err != nil {
		t.Fatal(err)
	}
	defer homeNAS.Close()
	homeNAS.LocalBotsFn = func() []string { return []string{"checkin", "mail"} }

	// 1. One-click Ed25519 short pairing code mutual handshake
	invite := sgVPS.CreatePairCode(5 * time.Minute)
	acceptReq := homeNAS.BuildPairAcceptRequest(invite.Code)
	sgRecord, err := sgVPS.AcceptPairRequest(acceptReq)
	if err != nil {
		t.Fatalf("AcceptPairRequest: %v", err)
	}
	if err := homeNAS.AddPeer(sgRecord); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	// Verify static directory is <= 400 chars and includes network tag
	dir := sgVPS.StaticDirectory()
	if !strings.Contains(dir, "home-nas [家庭宽带IP]: checkin,mail") || utf8.RuneCountInString(dir) > MaxStaticDirectoryChars {
		t.Fatalf("unexpected static directory: %q", dir)
	}

	// 2. Verify E2EE envelope rejects tampered ciphertext
	env, err := sgVPS.Identity.SealEnvelope(sgVPS.Peers()[0], "peer_message", []byte(`{"payload":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	tampered := env
	tampered.Ciphertext = tampered.Ciphertext + "AA"
	if _, err := homeNAS.Identity.OpenEnvelope(homeNAS.Peers()[0], tampered); err == nil {
		t.Fatal("tampered E2EE envelope must be rejected")
	}

	// 3. Connect homeNAS outbound to sgVPS over in-memory WebSocket pipe (NAT relay test)
	ln := newPipeListener()
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(sgVPS.ServeTunnelHTTP)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	homeNAS.Dialer = &websocket.Dialer{
		NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) { return ln.Dial() },
	}
	if err := homeNAS.ConnectToRelay(context.Background(), sgVPS.Identity.NodeID, "ws://sg.local/api/v2/mesh/tunnel"); err != nil {
		t.Fatalf("ConnectToRelay: %v", err)
	}

	// Wait briefly for heartbeat registration
	time.Sleep(50 * time.Millisecond)
	statuses := sgVPS.ListPeersStatus()
	if len(statuses) != 1 || !statuses[0].Online || statuses[0].NodeID != "home-nas" {
		t.Fatalf("expected home-nas online in RAM heartbeats, got %+v", statuses)
	}
	// Static directory MUST remain identical after dynamic heartbeat updates (zero Prompt Cache invalidation)
	if sgVPS.StaticDirectory() != dir {
		t.Fatal("dynamic heartbeat mutated static directory!")
	}

	// 4. Send taint-carrying peer message from overseas secretary -> checkin@home-nas
	recvCh := make(chan PeerMessage, 1)
	homeNAS.OnPeerMessage = func(msg PeerMessage) { recvCh <- msg }

	if _, err := sgVPS.SendPeerMessage(
		context.Background(),
		"secretary",
		"checkin@home-nas",
		"run_checkin",
		"请用国内家庭IP完成每日签到",
		true,
		true,
		[]string{"email:boss@co.com"},
	); err != nil {
		t.Fatalf("SendPeerMessage: %v", err)
	}

	select {
	case pm := <-recvCh:
		if pm.ToBot != "checkin@home-nas" || !pm.Tainted || len(pm.TaintSources) != 1 {
			t.Fatalf("unexpected received peer message: %+v", pm)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for peer message delivery")
	}
}

type pipeListener struct {
	ch     chan net.Conn
	closed chan struct{}
}

func newPipeListener() *pipeListener {
	return &pipeListener{ch: make(chan net.Conn, 4), closed: make(chan struct{})}
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
