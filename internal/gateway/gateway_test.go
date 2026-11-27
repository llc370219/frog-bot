package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"frogbot/internal/host"
	"frogbot/internal/mesh"
	"frogbot/internal/security"
	"github.com/gorilla/websocket"
)

func TestSinglePaneGatewayRoamingAndOfflinePush(t *testing.T) {
	// 1. Offline push notifier test (pluggable webhook + presence gating)
	var pushed []PushEvent
	var pushMu sync.Mutex
	notifier := NewOfflinePushNotifier(host.PushConfig{BarkURL: "http://mock.local/bark"}, nil)
	notifier.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
		}),
	}
	notifier.onSend = func(_ string, ev PushEvent) {
		pushMu.Lock()
		pushed = append(pushed, ev)
		pushMu.Unlock()
	}

	// 2. Mesh pair: home-gw (public Home Node) <-> home-nas (NAT node)
	homeGW, _ := mesh.OpenMesh(t.TempDir(), "home-gw", "[公网节点]", "")
	defer homeGW.Close()
	homeNAS, _ := mesh.OpenMesh(t.TempDir(), "home-nas", "[家庭宽带IP]", "")
	defer homeNAS.Close()

	inv := homeGW.CreatePairCode(5 * time.Minute)
	gwRec, err := homeGW.AcceptPairRequest(homeNAS.BuildPairAcceptRequest(inv.Code))
	if err != nil {
		t.Fatal(err)
	}
	_ = homeNAS.AddPeer(gwRec)

	// Wire Mesh tunnel over pipe listener
	meshLn := newPipeListener()
	defer meshLn.Close()
	meshSrv := &http.Server{Handler: http.HandlerFunc(homeGW.ServeTunnelHTTP)}
	go func() { _ = meshSrv.Serve(meshLn) }()
	defer meshSrv.Close()

	homeNAS.Dialer = &websocket.Dialer{NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) { return meshLn.Dial() }}
	if err := homeNAS.ConnectToRelay(context.Background(), homeGW.Identity.NodeID, "ws://gw.local/tunnel"); err != nil {
		t.Fatal(err)
	}

	var remoteDelivered string
	nasGW := NewSinglePaneGateway(HostCallbacks{
		NodeID: homeNAS.Identity.NodeID,
		SendChatMessage: func(_ context.Context, botID, text string) (any, error) {
			remoteDelivered = botID + ":" + text
			return map[string]string{"routed_to": botID, "node": "home-nas"}, nil
		},
	})
	homeNAS.OnGatewayProxy = nasGW.HandleMeshProxy

	secGW := security.NewSecurityGateway(nil, nil)
	mainGW := NewSinglePaneGateway(HostCallbacks{
		NodeID:          homeGW.Identity.NodeID,
		SecurityGateway: secGW,
		Mesh:            homeGW,
		PushNotifier:    notifier,
		ListTopology:    func() any { return map[string]any{"node_id": homeGW.Identity.NodeID, "peers": homeGW.ListPeersStatus()} },
	})

	// While NO client is connected, PushApprovalCard MUST trigger offline push!
	mainGW.PushApprovalCard(security.ApprovalCard{ID: "sec_1", BotID: "main", Action: "frog_exec", Summary: "测试审批", Reason: "新域名"})
	pushMu.Lock()
	if len(pushed) != 1 || pushed[0].Kind != "ASK_USER" {
		t.Fatalf("expected 1 offline push when disconnected, got %+v", pushed)
	}
	pushMu.Unlock()

	// Connect client over Single-Pane WebSocket to homeGW and transparently roam to checkin@home-nas!
	gwLn := newPipeListener()
	defer gwLn.Close()
	gwSrv := &http.Server{Handler: http.HandlerFunc(mainGW.ServeWS)}
	go func() { _ = gwSrv.Serve(gwLn) }()
	defer gwSrv.Close()

	d := websocket.Dialer{NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) { return gwLn.Dial() }}
	ws, _, err := d.Dial("ws://gw.local/api/v2/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	var hello Frame
	if err := ws.ReadJSON(&hello); err != nil || hello.Type != "hello" {
		t.Fatalf("expected hello frame, got %+v err=%v", hello, err)
	}

	// Roam chat.send to remote bot checkin@home-nas through single Home Node connection
	payload, _ := json.Marshal(map[string]string{"text": "帮我打卡"})
	if err := ws.WriteJSON(Frame{Type: "chat.send", ReqID: "r1", Target: "checkin@home-nas", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var ack Frame
	if err := ws.ReadJSON(&ack); err != nil || ack.Type != "chat.send.ack" || ack.Error != "" {
		t.Fatalf("expected routed chat.send.ack, got %+v err=%v", ack, err)
	}
	if remoteDelivered != "checkin:帮我打卡" {
		t.Fatalf("expected remote delivery checkin:帮我打卡, got %q", remoteDelivered)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
