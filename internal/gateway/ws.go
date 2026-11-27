package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"frogbot/internal/clipool"
	"frogbot/internal/mesh"
	"frogbot/internal/organs"
	"frogbot/internal/security"
	"github.com/gorilla/websocket"
)

// Frame is the multiplexed WebSocket envelope used by Mac / Android / Web clients
// connected to a single Home Node to control all local & remote `bot@node` instances.
type Frame struct {
	Type    string          `json:"type"`
	ReqID   string          `json:"req_id,omitempty"`
	Target  string          `json:"target,omitempty"` // "main" or "bot@node"
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// HostCallbacks bridges the Single-Pane WebSocket Gateway to the local Host Daemon services.
type HostCallbacks struct {
	NodeID           string
	ListTopology     func() any
	SendChatMessage  func(ctx context.Context, botID, text string) (any, error)
	SecurityGateway  *security.SecurityGateway
	CLIPool          *clipool.LocalCLIPool
	VerifyManager    *organs.ReadyToVerifyManager
	Mesh             *mesh.FrogLinkMesh
	PushNotifier     *OfflinePushNotifier
}

type wsClient struct {
	id         string
	ws         *websocket.Conn
	wmu        sync.Mutex
	foreground bool
	cliSubs    map[string]bool // taskIDs subscribed for live log chunks
	verifySubs map[string]bool // verify sessionIDs subscribed for CDP frames
}

func (c *wsClient) send(f Frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteJSON(f)
}

// SinglePaneGateway manages all connected Mac / Android / Web multiplexed WebSocket clients,
// transparently routing `bot@node` frames over FrogLink Mesh and triggering OfflinePushNotifier
// when urgent approvals/verifications/security denials fire while clients are backgrounded.
type SinglePaneGateway struct {
	cb HostCallbacks

	mu      sync.RWMutex
	clients map[*wsClient]bool
}

func NewSinglePaneGateway(cb HostCallbacks) *SinglePaneGateway {
	return &SinglePaneGateway{
		cb:      cb,
		clients: make(map[*wsClient]bool),
	}
}

// HasForegroundClient reports whether at least one client WS is actively online in foreground.
func (g *SinglePaneGateway) HasForegroundClient() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for c := range g.clients {
		if c.foreground {
			return true
		}
	}
	return false
}

// ClientCount reports the number of connected WebSocket clients.
func (g *SinglePaneGateway) ClientCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.clients)
}

// Broadcast sends a frame to every connected client.
func (g *SinglePaneGateway) Broadcast(typ string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	f := Frame{Type: typ, Payload: raw}
	g.mu.RLock()
	list := make([]*wsClient, 0, len(g.clients))
	for c := range g.clients {
		list = append(list, c)
	}
	g.mu.RUnlock()
	for _, c := range list {
		_ = c.send(f)
	}
}

// PushApprovalCard broadcasts an ASK_USER approval card to online clients and triggers offline push if backgrounded.
func (g *SinglePaneGateway) PushApprovalCard(card security.ApprovalCard) {
	g.Broadcast("approval.pending", card)
	if !g.HasForegroundClient() && g.cb.PushNotifier != nil {
		_ = g.cb.PushNotifier.Notify(context.Background(), PushEvent{
			Kind:     "ASK_USER",
			BotID:    card.BotID,
			NodeID:   g.cb.NodeID,
			Title:    "需要你的安全审批：" + card.Action,
			Body:     card.Summary + "（" + card.Reason + "）",
			ActionID: card.ID,
		})
	}
}

// PushDenyAlert broadcasts a DENY high-severity security alert and ALWAYS fires offline push too.
func (g *SinglePaneGateway) PushDenyAlert(in security.ReviewInput, dec security.ReviewDecision) {
	alert := map[string]any{"input": in, "decision": dec, "at": time.Now()}
	g.Broadcast("alert.security", alert)
	if g.cb.PushNotifier != nil {
		_ = g.cb.PushNotifier.Notify(context.Background(), PushEvent{
			Kind:   "DENY",
			BotID:  in.BotID,
			NodeID: g.cb.NodeID,
			Title:  "⚠️ 高危安全拦截：" + in.Action,
			Body:   dec.Reason,
		})
	}
}

// PushVerifyWaiting notifies clients that a Ready-to-Verify human checkpoint is waiting (0 MB browser RAM held).
func (g *SinglePaneGateway) PushVerifyWaiting(sess organs.VerifySession) {
	g.Broadcast("verify.waiting", sess)
	if !g.HasForegroundClient() && g.cb.PushNotifier != nil {
		_ = g.cb.PushNotifier.Notify(context.Background(), PushEvent{
			Kind:     "WAITING_HUMAN",
			BotID:    sess.BotID,
			NodeID:   g.cb.NodeID,
			Title:    "需要远程过验证（随点随验）",
			Body:     sess.Reason + " — " + sess.URL,
			ActionID: sess.ID,
		})
	}
}

// PushVerifyFrame pushes live CDP screencast JPEG frames to clients subscribed to sessionID.
func (g *SinglePaneGateway) PushVerifyFrame(frame organs.VerifyFrame) {
	raw, _ := json.Marshal(frame)
	f := Frame{Type: "verify.frame", Payload: raw}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for c := range g.clients {
		if len(c.verifySubs) == 0 || c.verifySubs[frame.SessionID] {
			_ = c.send(f)
		}
	}
}

// PushCLILogChunk streams live cli_<task_id>.log output to subscribed client CLI Log Drawers.
func (g *SinglePaneGateway) PushCLILogChunk(taskID, botID, chunk string) {
	raw, _ := json.Marshal(map[string]string{
		"task_id": taskID,
		"bot_id":  botID,
		"chunk":   chunk,
	})
	f := Frame{Type: "cli.log.chunk", Payload: raw}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for c := range g.clients {
		if c.cliSubs[taskID] || c.cliSubs["*"] {
			_ = c.send(f)
		}
	}
}

// ServeWS handles GET /api/v2/ws multiplexed connections from Mac / Android / Web clients.
func (g *SinglePaneGateway) ServeWS(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsClient{
		id:         r.RemoteAddr,
		ws:         ws,
		foreground: true,
		cliSubs:    map[string]bool{},
		verifySubs: map[string]bool{},
	}
	g.mu.Lock()
	g.clients[c] = true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.clients, c)
		g.mu.Unlock()
		_ = ws.Close()
	}()

	// Send initial hello + mesh topology snapshot
	hello := map[string]any{"node_id": g.cb.NodeID, "protocol": "frogbot-v2"}
	if g.cb.ListTopology != nil {
		hello["topology"] = g.cb.ListTopology()
	}
	if g.cb.SecurityGateway != nil {
		hello["pending_approvals"] = g.cb.SecurityGateway.Pending()
	}
	if g.cb.VerifyManager != nil {
		hello["pending_verifications"] = g.cb.VerifyManager.List("WAITING_HUMAN")
	}
	rawHello, _ := json.Marshal(hello)
	_ = c.send(Frame{Type: "hello", Payload: rawHello})

	for {
		var f Frame
		if err := ws.ReadJSON(&f); err != nil {
			return
		}
		g.handleClientFrame(r.Context(), c, f)
	}
}

func (g *SinglePaneGateway) handleClientFrame(ctx context.Context, c *wsClient, f Frame) {
	reply := func(typ string, payload any, err error) {
		resp := Frame{Type: typ, ReqID: f.ReqID, Target: f.Target}
		if err != nil {
			resp.Error = err.Error()
		} else if payload != nil {
			resp.Payload, _ = json.Marshal(payload)
		}
		_ = c.send(resp)
	}

	// Transparent cross-node Mesh roaming when target specifies `bot@remoteNode`!
	if botName, nodeID, ok := mesh.ParseBotAddress(f.Target); ok && nodeID != g.cb.NodeID && g.cb.Mesh != nil {
		fwd := f
		fwd.Target = botName
		raw, _ := json.Marshal(fwd)
		respRaw, err := g.cb.Mesh.RouteGatewayCall(ctx, nodeID, raw)
		if err != nil {
			reply(f.Type+".ack", nil, err)
			return
		}
		var remoteResp Frame
		if json.Unmarshal(respRaw, &remoteResp) == nil && remoteResp.Type != "" {
			remoteResp.ReqID = f.ReqID
			remoteResp.Target = f.Target
			_ = c.send(remoteResp)
			return
		}
		_ = c.send(Frame{Type: f.Type + ".ack", ReqID: f.ReqID, Target: f.Target, Payload: respRaw})
		return
	}

	switch f.Type {
	case "ping":
		reply("pong", map[string]int64{"ts": time.Now().UnixMilli()}, nil)

	case "presence":
		var p struct {
			Foreground bool `json:"foreground"`
		}
		if json.Unmarshal(f.Payload, &p) == nil {
			g.mu.Lock()
			c.foreground = p.Foreground
			g.mu.Unlock()
		}
		reply("presence.ack", map[string]bool{"ok": true}, nil)

	case "topology.get":
		if g.cb.ListTopology != nil {
			reply("topology", g.cb.ListTopology(), nil)
		}

	case "chat.send":
		var p struct {
			BotID string `json:"bot_id"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			reply("chat.send.ack", nil, err)
			return
		}
		if p.BotID == "" {
			p.BotID = strings.TrimSpace(f.Target)
		}
		if p.BotID == "" {
			p.BotID = "main"
		}
		if g.cb.SendChatMessage != nil {
			res, err := g.cb.SendChatMessage(ctx, p.BotID, p.Text)
			reply("chat.send.ack", res, err)
		}

	case "approval.decide":
		var p struct {
			CardID          string `json:"card_id"`
			Choice          string `json:"choice"` // allow_once | allow_whitelist | deny
			WhitelistDomain string `json:"whitelist_domain"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			reply("approval.decide.ack", nil, err)
			return
		}
		if g.cb.SecurityGateway != nil {
			err := g.cb.SecurityGateway.Resolve(p.CardID, security.UserApprovalChoice{
				Choice:          p.Choice,
				WhitelistDomain: p.WhitelistDomain,
				DecidedBy:       "user",
			})
			reply("approval.decide.ack", map[string]bool{"ok": err == nil}, err)
		}

	case "cli.log.subscribe":
		var p struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(f.Payload, &p) == nil && p.TaskID != "" {
			g.mu.Lock()
			c.cliSubs[p.TaskID] = true
			g.mu.Unlock()
			if g.cb.CLIPool != nil {
				if content, off, err := g.cb.CLIPool.ReadLog(p.TaskID, 0); err == nil {
					reply("cli.log.snapshot", map[string]any{"task_id": p.TaskID, "content": content, "offset": off}, nil)
					return
				}
			}
		}
		reply("cli.log.subscribe.ack", map[string]bool{"ok": true}, nil)

	case "cli.cancel":
		var p struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			reply("cli.cancel.ack", nil, err)
			return
		}
		if g.cb.CLIPool != nil {
			err := g.cb.CLIPool.Cancel(p.TaskID)
			reply("cli.cancel.ack", map[string]bool{"ok": err == nil}, err)
		}

	case "verify.ready":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			reply("verify.ready.ack", nil, err)
			return
		}
		g.mu.Lock()
		c.verifySubs[p.SessionID] = true
		g.mu.Unlock()
		if g.cb.VerifyManager != nil {
			err := g.cb.VerifyManager.StartLiveSession(context.Background(), p.SessionID, g.PushVerifyFrame)
			reply("verify.ready.ack", map[string]bool{"ok": err == nil}, err)
		}

	case "verify.input":
		var p struct {
			SessionID string                  `json:"session_id"`
			Event     organs.VerifyInputEvent `json:"event"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			reply("verify.input.ack", nil, err)
			return
		}
		if g.cb.VerifyManager != nil {
			err := g.cb.VerifyManager.DispatchInput(p.SessionID, p.Event)
			reply("verify.input.ack", map[string]bool{"ok": err == nil}, err)
		}

	case "verify.complete":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			reply("verify.complete.ack", nil, err)
			return
		}
		if g.cb.VerifyManager != nil {
			sess, err := g.cb.VerifyManager.CompleteSession(p.SessionID)
			reply("verify.complete.ack", sess, err)
		}

	case "verify.cancel":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			reply("verify.cancel.ack", nil, err)
			return
		}
		if g.cb.VerifyManager != nil {
			sess, _ := g.cb.VerifyManager.StopLiveSession(p.SessionID, "CANCELLED")
			reply("verify.cancel.ack", sess, nil)
		}

	case "push.register":
		var p struct {
			UnifiedPushURL string `json:"unified_push_url"`
			FCMDeviceToken string `json:"fcm_device_token"`
			BarkURL        string `json:"bark_url"`
		}
		if json.Unmarshal(f.Payload, &p) == nil && g.cb.PushNotifier != nil {
			g.cb.PushNotifier.RegisterMobileEndpoint(p.UnifiedPushURL, p.FCMDeviceToken, p.BarkURL)
		}
		reply("push.register.ack", map[string]bool{"ok": true}, nil)
	}
}

// HandleMeshProxy executes an incoming forwarded Gateway Frame received over FrogLink Mesh.
func (g *SinglePaneGateway) HandleMeshProxy(ctx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
	var f Frame
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	switch f.Type {
	case "chat.send":
		var p struct {
			BotID string `json:"bot_id"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, err
		}
		if p.BotID == "" {
			p.BotID = f.Target
		}
		res, err := g.cb.SendChatMessage(ctx, p.BotID, p.Text)
		if err != nil {
			return nil, err
		}
		out, _ := json.Marshal(res)
		return json.Marshal(Frame{Type: "chat.send.ack", Payload: out})
	default:
		return json.Marshal(Frame{Type: f.Type + ".ack", Payload: json.RawMessage(`{"ok":true}`)})
	}
}
