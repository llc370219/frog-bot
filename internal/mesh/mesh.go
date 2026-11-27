package mesh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"frogbot/internal/atomicfile"
	"frogbot/internal/clipool"
	"github.com/gorilla/websocket"
)

const MaxStaticDirectoryChars = 400

// PeerMessage is the decrypted cross-node bot-to-bot message payload carrying Taint provenance.
type PeerMessage struct {
	ID           string    `json:"id"`
	FromBot      string    `json:"from_bot"` // bot@node
	ToBot        string    `json:"to_bot"`   // bot@node
	Intent       string    `json:"intent"`
	Payload      string    `json:"payload"`
	NeedReply    bool      `json:"need_reply"`
	ReplyTo      string    `json:"reply_to,omitempty"`
	Tainted      bool      `json:"tainted"`
	TaintSources []string  `json:"taint_sources,omitempty"`
	SentAt       time.Time `json:"sent_at"`
}

// PeerLiveStatus holds dynamic per-minute telemetry strictly in Host RAM (never injected into System Prompt!).
type PeerLiveStatus struct {
	NodeID       string                `json:"node_id"`
	Name         string                `json:"name"`
	NetworkTag   string                `json:"network_tag,omitempty"`
	Online       bool                  `json:"online"`
	ConnectedVia string                `json:"connected_via,omitempty"` // "tunnel" | "direct" | "relay"
	LatencyMs    int64                 `json:"latency_ms"`
	Bots         []string              `json:"bots"`
	CLIQuotas    []clipool.QuotaStatus `json:"cli_quotas,omitempty"`
	LastSeen     time.Time             `json:"last_seen"`
}

// PairInvite is a short one-time pairing code issued by a node for one-click mutual Ed25519 trust.
type PairInvite struct {
	Code       string    `json:"code"`
	NodeID     string    `json:"node_id"`
	Name       string    `json:"name"`
	NetworkTag string    `json:"network_tag,omitempty"`
	PublicURL  string    `json:"public_url,omitempty"`
	Ed25519Pub string    `json:"ed25519_pub"`
	X25519Pub  string    `json:"x25519_pub"`
	Bots       []string  `json:"bots,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// PairAcceptRequest is submitted by the joining node to redeem a PairInvite.
type PairAcceptRequest struct {
	Code       string   `json:"code"`
	NodeID     string   `json:"node_id"`
	Name       string   `json:"name"`
	NetworkTag string   `json:"network_tag,omitempty"`
	PublicURL  string   `json:"public_url,omitempty"`
	Ed25519Pub string   `json:"ed25519_pub"`
	X25519Pub  string   `json:"x25519_pub"`
	Bots       []string `json:"bots,omitempty"`
	Timestamp  int64    `json:"timestamp"`
	Signature  string   `json:"signature"`
}

// TunnelFrame is multiplexed over FrogLink WebSocket tunnels between NAT and public nodes.
type TunnelFrame struct {
	Type      string             `json:"type"` // "envelope" | "heartbeat" | "gateway_req" | "gateway_resp"
	Envelope  *EncryptedEnvelope `json:"envelope,omitempty"`
	Heartbeat *PeerLiveStatus    `json:"heartbeat,omitempty"`
	ReqID     string             `json:"req_id,omitempty"`
	ToNode    string             `json:"to_node,omitempty"`
	FromNode  string             `json:"from_node,omitempty"`
	Body      json.RawMessage    `json:"body,omitempty"`
}

type peerTunnel struct {
	nodeID string
	ws     *websocket.Conn
	wmu    sync.Mutex
}

func (pt *peerTunnel) send(frame TunnelFrame) error {
	pt.wmu.Lock()
	defer pt.wmu.Unlock()
	return pt.ws.WriteJSON(frame)
}

// GatewayProxyHandler handles remote gateway commands routed to this node via FrogLink Mesh.
type GatewayProxyHandler func(ctx context.Context, fromNode string, raw json.RawMessage) (json.RawMessage, error)

// FrogLinkMesh coordinates Ed25519 pairing, NAT relay tunnels, cache-safe static directories,
// RAM-only heartbeats, taint-preserving bot@node messaging, and remote gateway roaming.
type FrogLinkMesh struct {
	Identity     *NodeIdentity
	NodeName     string
	NetworkTag   string
	PublicURL    string
	TopologyPath string
	LocalBotsFn  func() []string
	LocalQuotaFn func() []clipool.QuotaStatus
	Dialer       *websocket.Dialer // overridable for in-memory tests
	HTTPClient   *http.Client

	OnPeerMessage  func(msg PeerMessage)
	OnGatewayProxy GatewayProxyHandler

	mu          sync.RWMutex
	peers       map[string]PeerRecord
	invites     map[string]PairInvite
	live        map[string]PeerLiveStatus
	tunnels     map[string]*peerTunnel
	pendingRPC  map[string]chan json.RawMessage
	cancelConns []context.CancelFunc
	wg          sync.WaitGroup
}

func OpenMesh(hostDir, nodeName, networkTag, publicURL string) (*FrogLinkMesh, error) {
	id, err := LoadOrCreateIdentity(filepath.Join(hostDir, "node_key.json"), nodeName)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(nodeName) == "" {
		nodeName = id.NodeID
	}
	m := &FrogLinkMesh{
		Identity:     id,
		NodeName:     nodeName,
		NetworkTag:   networkTag,
		PublicURL:    strings.TrimRight(publicURL, "/"),
		TopologyPath: filepath.Join(hostDir, "topology.json"),
		Dialer:       websocket.DefaultDialer,
		HTTPClient:   &http.Client{Timeout: 15 * time.Second},
		peers:        make(map[string]PeerRecord),
		invites:      make(map[string]PairInvite),
		live:         make(map[string]PeerLiveStatus),
		tunnels:      make(map[string]*peerTunnel),
		pendingRPC:   make(map[string]chan json.RawMessage),
	}
	_ = m.loadTopology()
	return m, nil
}

func (m *FrogLinkMesh) Close() {
	m.mu.Lock()
	for _, c := range m.cancelConns {
		c()
	}
	for _, t := range m.tunnels {
		_ = t.ws.Close()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *FrogLinkMesh) loadTopology() error {
	raw, err := os.ReadFile(m.TopologyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []PeerRecord
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	m.mu.Lock()
	for _, p := range list {
		m.peers[p.NodeID] = p
	}
	m.mu.Unlock()
	return nil
}

func (m *FrogLinkMesh) saveTopologyLocked() error {
	list := make([]PeerRecord, 0, len(m.peers))
	for _, p := range m.peers {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].NodeID < list[j].NodeID })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.TopologyPath), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(m.TopologyPath, raw, 0o600)
}

func (m *FrogLinkMesh) localBots() []string {
	if m.LocalBotsFn != nil {
		return m.LocalBotsFn()
	}
	return []string{"main"}
}

// CreatePairCode issues a short one-time pairing code (valid 10m) for Ed25519 mutual trust.
func (m *FrogLinkMesh) CreatePairCode(ttl time.Duration) PairInvite {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	hx := strings.ToUpper(hex.EncodeToString(b[:]))
	code := fmt.Sprintf("FROG-%s-%s", hx[:4], hx[4:])
	inv := PairInvite{
		Code:       code,
		NodeID:     m.Identity.NodeID,
		Name:       m.NodeName,
		NetworkTag: m.NetworkTag,
		PublicURL:  m.PublicURL,
		Ed25519Pub: m.Identity.Ed25519Pub,
		X25519Pub:  m.Identity.X25519Pub,
		Bots:       m.localBots(),
		ExpiresAt:  time.Now().Add(ttl),
	}
	m.mu.Lock()
	m.invites[code] = inv
	m.mu.Unlock()
	return inv
}

// BuildPairAcceptRequest builds a signed request to redeem a remote node's pairing code.
func (m *FrogLinkMesh) BuildPairAcceptRequest(code string) PairAcceptRequest {
	ts := time.Now().Unix()
	req := PairAcceptRequest{
		Code:       strings.TrimSpace(code),
		NodeID:     m.Identity.NodeID,
		Name:       m.NodeName,
		NetworkTag: m.NetworkTag,
		PublicURL:  m.PublicURL,
		Ed25519Pub: m.Identity.Ed25519Pub,
		X25519Pub:  m.Identity.X25519Pub,
		Bots:       m.localBots(),
		Timestamp:  ts,
	}
	req.Signature = m.Identity.Sign(pairSignPayload(req.Code, req.NodeID, req.Timestamp))
	return req
}

// AcceptPairRequest validates a joining peer's pairing code + Ed25519 signature, stores mutual trust,
// and returns this node's PeerRecord so the joining peer can save it too.
func (m *FrogLinkMesh) AcceptPairRequest(req PairAcceptRequest) (PeerRecord, error) {
	m.mu.Lock()
	inv, ok := m.invites[strings.TrimSpace(req.Code)]
	if !ok || time.Now().After(inv.ExpiresAt) {
		delete(m.invites, req.Code)
		m.mu.Unlock()
		return PeerRecord{}, errors.New("配对码无效或已过期")
	}
	if !VerifyPeerSignature(req.Ed25519Pub, pairSignPayload(req.Code, req.NodeID, req.Timestamp), req.Signature) {
		m.mu.Unlock()
		return PeerRecord{}, errors.New("对端 Ed25519 配对签名校验失败")
	}
	delete(m.invites, req.Code)
	peer := PeerRecord{
		NodeID:     req.NodeID,
		Name:       req.Name,
		NetworkTag: req.NetworkTag,
		PublicURL:  strings.TrimRight(req.PublicURL, "/"),
		Ed25519Pub: req.Ed25519Pub,
		X25519Pub:  req.X25519Pub,
		Bots:       req.Bots,
		PairedAt:   time.Now(),
	}
	m.peers[peer.NodeID] = peer
	_ = m.saveTopologyLocked()
	m.mu.Unlock()

	return PeerRecord{
		NodeID:     m.Identity.NodeID,
		Name:       m.NodeName,
		NetworkTag: m.NetworkTag,
		PublicURL:  m.PublicURL,
		Ed25519Pub: m.Identity.Ed25519Pub,
		X25519Pub:  m.Identity.X25519Pub,
		Bots:       m.localBots(),
		PairedAt:   time.Now(),
	}, nil
}

// AddPeer saves a trusted PeerRecord into topology.json (used by the joining side after pairing).
func (m *FrogLinkMesh) AddPeer(peer PeerRecord) error {
	if peer.NodeID == "" || peer.NodeID == m.Identity.NodeID {
		return errors.New("无效的对端 node_id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peers[peer.NodeID] = peer
	return m.saveTopologyLocked()
}

// Peers returns all statically paired peer nodes.
func (m *FrogLinkMesh) Peers() []PeerRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PeerRecord, 0, len(m.peers))
	for _, p := range m.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// StaticDirectory builds the <=400-char static peer roster frozen into the System Prompt prefix.
// Intentionally excludes all dynamic latency/heartbeat/quota data so Prompt Cache NEVER breaks!
func (m *FrogLinkMesh) StaticDirectory() string {
	peers := m.Peers()
	if len(peers) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, p := range peers {
		tag := p.NetworkTag
		if tag != "" {
			tag = " " + tag
		}
		bots := strings.Join(p.Bots, ",")
		if bots == "" {
			bots = "main"
		}
		line := fmt.Sprintf("- %s%s: %s\n", p.NodeID, tag, bots)
		if utf8.RuneCountInString(sb.String()+line) > MaxStaticDirectoryChars {
			break
		}
		sb.WriteString(line)
	}
	return strings.TrimSpace(sb.String())
}

// ListPeersStatus returns live RAM-only telemetry for the on-demand `list_peers_status()` tool.
func (m *FrogLinkMesh) ListPeersStatus() []PeerLiveStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PeerLiveStatus, 0, len(m.peers))
	for _, p := range m.peers {
		st, ok := m.live[p.NodeID]
		if !ok {
			st = PeerLiveStatus{
				NodeID:     p.NodeID,
				Name:       p.Name,
				NetworkTag: p.NetworkTag,
				Online:     false,
				Bots:       p.Bots,
			}
		}
		if _, hasTunnel := m.tunnels[p.NodeID]; hasTunnel {
			st.Online = true
			if st.ConnectedVia == "" {
				st.ConnectedVia = "tunnel"
			}
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// ServeTunnelHTTP upgrades an inbound NAT-peer WebSocket connection on `/api/v2/mesh/tunnel`,
// authenticating via Ed25519 headers and registering it for automatic E2EE envelope relay.
func (m *FrogLinkMesh) ServeTunnelHTTP(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.Header.Get("X-Frog-Node"))
	tsStr := strings.TrimSpace(r.Header.Get("X-Frog-TS"))
	sig := strings.TrimSpace(r.Header.Get("X-Frog-Sig"))

	m.mu.RLock()
	peer, ok := m.peers[nodeID]
	m.mu.RUnlock()
	if !ok {
		http.Error(w, "unknown peer node", http.StatusUnauthorized)
		return
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil || absInt64(time.Now().Unix()-ts) > 300 {
		http.Error(w, "stale timestamp", http.StatusUnauthorized)
		return
	}
	if !VerifyPeerSignature(peer.Ed25519Pub, []byte("tunnel|"+nodeID+"|"+tsStr), sig) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	pt := &peerTunnel{nodeID: nodeID, ws: ws}
	m.registerTunnel(nodeID, pt)
	defer m.unregisterTunnel(nodeID, pt)

	m.readTunnelLoop(r.Context(), nodeID, pt)
}

// ConnectToRelay connects a NAT/home node outbound to a paired public node's `/api/v2/mesh/tunnel`
// and maintains periodic 60s RAM-only heartbeats.
func (m *FrogLinkMesh) ConnectToRelay(parentCtx context.Context, relayPeerNodeID, wsURL string) error {
	ctx, cancel := context.WithCancel(parentCtx)
	m.mu.Lock()
	m.cancelConns = append(m.cancelConns, cancel)
	m.mu.Unlock()

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	hdr := http.Header{}
	hdr.Set("X-Frog-Node", m.Identity.NodeID)
	hdr.Set("X-Frog-TS", ts)
	hdr.Set("X-Frog-Sig", m.Identity.Sign([]byte("tunnel|"+m.Identity.NodeID+"|"+ts)))

	d := m.Dialer
	if d == nil {
		d = websocket.DefaultDialer
	}
	ws, _, err := d.DialContext(ctx, wsURL, hdr)
	if err != nil {
		cancel()
		return err
	}
	pt := &peerTunnel{nodeID: relayPeerNodeID, ws: ws}
	m.registerTunnel(relayPeerNodeID, pt)

	// Send immediate heartbeat
	_ = pt.send(TunnelFrame{Type: "heartbeat", Heartbeat: m.buildHeartbeat()})

	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		defer m.unregisterTunnel(relayPeerNodeID, pt)
		defer ws.Close()
		m.readTunnelLoop(ctx, relayPeerNodeID, pt)
	}()
	go func() {
		defer m.wg.Done()
		tk := time.NewTicker(60 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = ws.Close()
				return
			case <-tk.C:
				if err := pt.send(TunnelFrame{Type: "heartbeat", Heartbeat: m.buildHeartbeat()}); err != nil {
					return
				}
			}
		}
	}()
	return nil
}

func (m *FrogLinkMesh) buildHeartbeat() *PeerLiveStatus {
	var quotas []clipool.QuotaStatus
	if m.LocalQuotaFn != nil {
		quotas = m.LocalQuotaFn()
	}
	return &PeerLiveStatus{
		NodeID:     m.Identity.NodeID,
		Name:       m.NodeName,
		NetworkTag: m.NetworkTag,
		Online:     true,
		Bots:       m.localBots(),
		CLIQuotas:  quotas,
		LastSeen:   time.Now(),
	}
}

func (m *FrogLinkMesh) registerTunnel(nodeID string, pt *peerTunnel) {
	m.mu.Lock()
	m.tunnels[nodeID] = pt
	st := m.live[nodeID]
	st.NodeID = nodeID
	st.Online = true
	st.ConnectedVia = "tunnel"
	st.LastSeen = time.Now()
	if p, ok := m.peers[nodeID]; ok {
		st.Name = p.Name
		st.NetworkTag = p.NetworkTag
		if len(st.Bots) == 0 {
			st.Bots = p.Bots
		}
	}
	m.live[nodeID] = st
	m.mu.Unlock()
}

func (m *FrogLinkMesh) unregisterTunnel(nodeID string, pt *peerTunnel) {
	m.mu.Lock()
	if m.tunnels[nodeID] == pt {
		delete(m.tunnels, nodeID)
		if st, ok := m.live[nodeID]; ok {
			st.Online = false
			m.live[nodeID] = st
		}
	}
	m.mu.Unlock()
}

func (m *FrogLinkMesh) readTunnelLoop(ctx context.Context, fromTunnelNode string, pt *peerTunnel) {
	for ctx.Err() == nil {
		var frame TunnelFrame
		if err := pt.ws.ReadJSON(&frame); err != nil {
			return
		}
		switch frame.Type {
		case "heartbeat":
			if frame.Heartbeat != nil {
				m.mu.Lock()
				hb := *frame.Heartbeat
				hb.NodeID = fromTunnelNode
				hb.Online = true
				hb.ConnectedVia = "tunnel"
				hb.LastSeen = time.Now()
				m.live[fromTunnelNode] = hb
				m.mu.Unlock()
			}
		case "envelope":
			if frame.Envelope == nil {
				continue
			}
			env := *frame.Envelope
			if env.ToNode == m.Identity.NodeID {
				_ = m.DeliverLocalEnvelope(env)
			} else {
				// Automatic NAT Relay: forward opaque E2EE envelope to destination tunnel!
				m.mu.RLock()
				dst := m.tunnels[env.ToNode]
				m.mu.RUnlock()
				if dst != nil {
					_ = dst.send(TunnelFrame{Type: "envelope", Envelope: &env})
				}
			}
		case "gateway_req":
			if frame.ToNode != "" && frame.ToNode != m.Identity.NodeID {
				m.mu.RLock()
				dst := m.tunnels[frame.ToNode]
				m.mu.RUnlock()
				if dst != nil {
					_ = dst.send(frame)
				}
				continue
			}
			if m.OnGatewayProxy != nil {
				go func(fr TunnelFrame) {
					respBody, err := m.OnGatewayProxy(ctx, fr.FromNode, fr.Body)
					if err != nil {
						respBody, _ = json.Marshal(map[string]string{"error": err.Error()})
					}
					_ = pt.send(TunnelFrame{
						Type:     "gateway_resp",
						ReqID:    fr.ReqID,
						FromNode: m.Identity.NodeID,
						ToNode:   fr.FromNode,
						Body:     respBody,
					})
				}(frame)
			}
		case "gateway_resp":
			if frame.ToNode != "" && frame.ToNode != m.Identity.NodeID {
				m.mu.RLock()
				dst := m.tunnels[frame.ToNode]
				m.mu.RUnlock()
				if dst != nil {
					_ = dst.send(frame)
				}
				continue
			}
			m.mu.Lock()
			ch := m.pendingRPC[frame.ReqID]
			delete(m.pendingRPC, frame.ReqID)
			m.mu.Unlock()
			if ch != nil {
				ch <- frame.Body
			}
		}
	}
}

// DeliverLocalEnvelope verifies & decrypts an inbound E2EE envelope addressed to this node.
func (m *FrogLinkMesh) DeliverLocalEnvelope(env EncryptedEnvelope) error {
	m.mu.RLock()
	sender, ok := m.peers[env.FromNode]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("未配对来源节点: %s", env.FromNode)
	}
	pt, err := m.Identity.OpenEnvelope(sender, env)
	if err != nil {
		return err
	}
	if env.Kind == "peer_message" {
		var pm PeerMessage
		if err := json.Unmarshal(pt, &pm); err != nil {
			return err
		}
		if m.OnPeerMessage != nil {
			m.OnPeerMessage(pm)
		}
	}
	return nil
}

// SendPeerMessage encrypts and routes a taint-carrying message to `target` ("bot@node").
func (m *FrogLinkMesh) SendPeerMessage(ctx context.Context, fromBot, target, intent, payload string, needReply, tainted bool, taintSources []string) (PeerMessage, error) {
	botName, targetNode, ok := ParseBotAddress(target)
	if !ok {
		return PeerMessage{}, fmt.Errorf("目标地址须为 bot@node 格式，收到: %s", target)
	}
	m.mu.RLock()
	peer, hasPeer := m.peers[targetNode]
	directTunnel := m.tunnels[targetNode]
	var relayTunnel *peerTunnel
	if directTunnel == nil {
		if peer.RelayVia != "" {
			relayTunnel = m.tunnels[peer.RelayVia]
		}
		if relayTunnel == nil {
			// Pick any connected tunnel (e.g. home node's uplink to public relay)
			for _, t := range m.tunnels {
				relayTunnel = t
				break
			}
		}
	}
	m.mu.RUnlock()
	if !hasPeer {
		return PeerMessage{}, fmt.Errorf("目标节点 %s 尚未配对", targetNode)
	}

	var idB [6]byte
	_, _ = rand.Read(idB[:])
	pm := PeerMessage{
		ID:           "pm_" + hex.EncodeToString(idB[:]),
		FromBot:      fromBot + "@" + m.Identity.NodeID,
		ToBot:        botName + "@" + targetNode,
		Intent:       intent,
		Payload:      payload,
		NeedReply:    needReply,
		Tainted:      tainted,
		TaintSources: append([]string(nil), taintSources...),
		SentAt:       time.Now(),
	}
	raw, err := json.Marshal(pm)
	if err != nil {
		return PeerMessage{}, err
	}
	env, err := m.Identity.SealEnvelope(peer, "peer_message", raw)
	if err != nil {
		return PeerMessage{}, err
	}

	// Route priority: 1) direct active tunnel, 2) relay tunnel, 3) direct HTTPS POST
	if directTunnel != nil {
		return pm, directTunnel.send(TunnelFrame{Type: "envelope", Envelope: &env})
	}
	if relayTunnel != nil {
		return pm, relayTunnel.send(TunnelFrame{Type: "envelope", Envelope: &env})
	}
	if peer.PublicURL != "" {
		body, _ := json.Marshal(env)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.PublicURL+"/api/v2/mesh/deliver", bytes.NewReader(body))
		if err != nil {
			return PeerMessage{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := m.HTTPClient.Do(req)
		if err != nil {
			return PeerMessage{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return PeerMessage{}, fmt.Errorf("对端投递失败 HTTP %d", resp.StatusCode)
		}
		return pm, nil
	}
	return PeerMessage{}, fmt.Errorf("节点 %s 当前离线且无可用中继隧道", targetNode)
}

// RouteGatewayCall transparently forwards a Single-Pane Gateway command to a remote node over the Mesh.
func (m *FrogLinkMesh) RouteGatewayCall(ctx context.Context, targetNode string, body json.RawMessage) (json.RawMessage, error) {
	m.mu.RLock()
	dst := m.tunnels[targetNode]
	if dst == nil {
		for _, t := range m.tunnels {
			dst = t
			break
		}
	}
	m.mu.RUnlock()
	if dst == nil {
		return nil, fmt.Errorf("节点 %s 当前无活跃隧道", targetNode)
	}
	var rb [6]byte
	_, _ = rand.Read(rb[:])
	reqID := "rpc_" + hex.EncodeToString(rb[:])
	ch := make(chan json.RawMessage, 1)
	m.mu.Lock()
	m.pendingRPC[reqID] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pendingRPC, reqID)
		m.mu.Unlock()
	}()

	if err := dst.send(TunnelFrame{
		Type:     "gateway_req",
		ReqID:    reqID,
		FromNode: m.Identity.NodeID,
		ToNode:   targetNode,
		Body:     body,
	}); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ParseBotAddress splits "bot@node" into ("bot", "node", true).
func ParseBotAddress(addr string) (botName, nodeID string, ok bool) {
	addr = strings.TrimSpace(addr)
	b, n, found := strings.Cut(addr, "@")
	if !found || strings.TrimSpace(b) == "" || strings.TrimSpace(n) == "" {
		return "", "", false
	}
	return strings.TrimSpace(b), strings.TrimSpace(n), true
}

func pairSignPayload(code, nodeID string, ts int64) []byte {
	return []byte(fmt.Sprintf("pair|%s|%s|%d", code, nodeID, ts))
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
