package bot

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"frogbot/internal/onefrog"
)

// 客户端互联：Mac / 安卓客户端同时登录了几台服务器时，可以在这几台服务器的 Bot 之间转发文字消息
// （ask_agent / message_agent）。服务器之间不直接连接，也不保存任何互联配置：
//
//   - 客户端开着时每隔一会儿调用 POST /api/relay/sync，告诉服务器「本机哪些 Bot 参与互联」和
//     「其他服务器上哪些 Bot 可以联系」，同时取走要转发的消息（长轮询）。
//   - 客户端把消息转给目标服务器的 POST /api/relay/inbox，目标 Bot 像收到远程设备消息一样被唤醒。
//   - 客户端关掉或关闭开关后，过 relayTTL 这些地址就从通讯录里消失，发不出去的消息直接报错，不排队。
//
// 只传文字：对方收到后要不要做事、在哪做、要不要审批，都由对方自己的机器决定。

const (
	relayTTL      = 45 * time.Second
	relayPoll     = 25 * time.Second
	relayDeliver  = 12 * time.Second
	relayMaxPeers = 200
)

var (
	relayAddress  = regexp.MustCompile(`^relay:[a-z0-9]{1,16}/(main|bot_[a-f0-9]{16})$`)
	relayClientID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

func isRelayAddress(a string) bool { return strings.HasPrefix(a, "relay:") }

type relayPeer struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	Server  string `json:"server"`
}

// relayItem 是一条等客户端取走转发的消息；From 是本机 Bot ID，To 是 relay 地址。
type relayItem struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Text       string `json:"text"`
	RequestID  string `json:"request_id"`
	ReplyTo    string `json:"reply_to,omitempty"`
	WantsReply bool   `json:"wants_reply,omitempty"`
	handed     bool
	done       chan error
}

type relayClient struct {
	peers   map[string]relayPeer // relay 地址 → 对方
	allowed map[string]bool      // 本机参与互联的 Bot
	seen    time.Time
	outbox  []*relayItem
	notify  chan struct{}
}

// close 让还在等的消息立刻失败，并叫醒正在长轮询的请求（它会拿到空列表返回）。
func (c *relayClient) close(err error) {
	for _, it := range c.outbox {
		it.done <- err
	}
	c.outbox = nil
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

type relayHub struct {
	mu      sync.Mutex
	clients map[string]*relayClient
}

func (h *relayHub) pruneLocked(now time.Time) {
	for id, c := range h.clients {
		if now.Sub(c.seen) > relayTTL {
			c.close(errors.New("客户端已经关闭，消息没有送到"))
			delete(h.clients, id)
		}
	}
}

// peersFor 列出本机这个 Bot 现在能通过客户端联系到的 Bot（多个客户端时合并去重）。
func (h *relayHub) peersFor(bot string) []relayPeer {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked(time.Now())
	seen := map[string]bool{}
	var out []relayPeer
	for _, c := range h.clients {
		if !c.allowed[bot] {
			continue
		}
		for addr, p := range c.peers {
			if !seen[addr] {
				seen[addr] = true
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// route 找一个能把 from 的消息送到 addr 的客户端（最近活跃的优先）。
func (h *relayHub) routeLocked(from, addr string) *relayClient {
	var best *relayClient
	for _, c := range h.clients {
		if c.allowed[from] {
			if _, ok := c.peers[addr]; ok && (best == nil || c.seen.After(best.seen)) {
				best = c
			}
		}
	}
	return best
}

func (bs *Bots) deliverRelay(from, address, text, replyTo string, wantsReply bool) (string, string, error) {
	if !relayAddress.MatchString(address) {
		return "", "", errors.New("客户端互联地址无效，请先用 list_agents 查看")
	}
	b, release := bs.acquireBot(from)
	defer release()
	if b == nil {
		return "", "", errors.New("发送方 Bot 不存在")
	}
	text = strings.TrimSpace(b.vault.Redact(text))
	if text == "" || len(text) > 8000 {
		return "", "", errors.New("消息不能为空且最多 8000 字节")
	}
	it := &relayItem{ID: onefrog.NewID("msg"), From: from, To: address, Text: text,
		RequestID: onefrog.NewID("req"), WantsReply: wantsReply, done: make(chan error, 1)}
	var original Message
	if replyTo != "" {
		for _, m := range b.store.Inbox(true) {
			if m.FromBotID == address && m.ToBotID == from && m.RequestID == replyTo &&
				m.WantsReply && m.MessageState == "pending" && m.Hop == 1 {
				original = m
				break
			}
		}
		if original.ID == "" {
			return "", "", errors.New("reply_to 未匹配到该 Bot 发来的待处理请求")
		}
		it.RequestID, it.ReplyTo, it.WantsReply = replyTo, replyTo, false
	}

	h := &bs.relay
	h.mu.Lock()
	h.pruneLocked(time.Now())
	c := h.routeLocked(from, address)
	if c == nil {
		h.mu.Unlock()
		return "", "", errors.New("联系不上 " + address + "：只有用户的客户端开着、并且双方都打开了互联开关时才能发消息")
	}
	hop := 1
	if replyTo != "" {
		hop = 2
	}
	// 先记下发出的消息：对方可能很快回复，回复要能匹配上这条请求。
	b.store.Append("message", Message{ID: it.ID, Role: "notice", Text: it.Text, FromBotID: from,
		ToBotID: address, RequestID: it.RequestID, ReplyTo: it.ReplyTo, WantsReply: it.WantsReply,
		MessageState: "sent", Hop: hop, Silent: true, CreatedAt: time.Now()}, true)
	c.outbox = append(c.outbox, it)
	select {
	case c.notify <- struct{}{}:
	default:
	}
	h.mu.Unlock()

	var err error
	select {
	case err = <-it.done:
	case <-time.After(relayDeliver):
		err = errors.New("客户端没有及时转发，消息没有送到")
		h.mu.Lock()
		h.dropLocked(it)
		h.mu.Unlock()
	case <-b.ctx.Done():
		err = b.ctx.Err()
	}
	if err != nil {
		bs.deliveryMu.Lock()
		if sent, ok := b.store.Message(it.ID); ok && sent.MessageState == "sent" {
			b.store.Append("message.state", map[string]string{"ID": it.ID, "State": "delivery_failed"}, true)
		}
		bs.deliveryMu.Unlock()
		return "", "", err
	}
	if original.ID != "" {
		b.store.Append("message.state", map[string]string{"ID": original.ID, "State": "handled"}, true)
	}
	return it.ID, it.RequestID, nil
}

func (h *relayHub) dropLocked(it *relayItem) {
	for _, c := range h.clients {
		for i, x := range c.outbox {
			if x == it {
				c.outbox = append(c.outbox[:i], c.outbox[i+1:]...)
				return
			}
		}
	}
}

type relaySyncRequest struct {
	ClientID string      `json:"client_id"`
	Enabled  bool        `json:"enabled"`
	Allowed  []string    `json:"allowed"`
	Peers    []relayPeer `json:"peers"`
	Acks     []struct {
		ID    string `json:"id"`
		Error string `json:"error,omitempty"`
	} `json:"acks"`
	Wait bool `json:"wait"`
}

// serveRelaySync：客户端的心跳 + 通讯录 + 取走待转发消息（有消息立刻返回，没有就最多等 relayPoll）。
func (bs *Bots) serveRelaySync(w http.ResponseWriter, r *http.Request) {
	var req relaySyncRequest
	if !decodeJSON(w, r, &req, 256<<10) {
		return
	}
	if !relayClientID.MatchString(req.ClientID) || len(req.Peers) > relayMaxPeers || len(req.Allowed) > maxBots+1 {
		writeJSON(w, nil, errors.New("互联同步请求无效"))
		return
	}
	h := &bs.relay
	h.mu.Lock()
	if h.clients == nil {
		h.clients = map[string]*relayClient{}
	}
	c := h.clients[req.ClientID]
	// 先处理上一轮的转发结果
	if c != nil {
		for _, a := range req.Acks {
			for i, it := range c.outbox {
				if it.ID == a.ID {
					if a.Error != "" {
						it.done <- errors.New(a.Error)
					} else {
						it.done <- nil
					}
					c.outbox = append(c.outbox[:i], c.outbox[i+1:]...)
					break
				}
			}
		}
	}
	if !req.Enabled {
		if c != nil {
			for _, it := range c.outbox {
				it.done <- errors.New("客户端关闭了互联，消息没有送到")
			}
			delete(h.clients, req.ClientID)
		}
		h.mu.Unlock()
		writeJSON(w, map[string]any{"items": []relayItem{}}, nil)
		return
	}
	if c == nil {
		c = &relayClient{notify: make(chan struct{}, 1)}
		h.clients[req.ClientID] = c
	}
	c.seen = time.Now()
	c.allowed = map[string]bool{}
	for _, id := range req.Allowed {
		if validBotID(id) {
			c.allowed[id] = true
		}
	}
	c.peers = map[string]relayPeer{}
	for _, p := range req.Peers {
		p.Name, p.Server = clip(strings.TrimSpace(p.Name), 80), clip(strings.TrimSpace(p.Server), 80)
		if relayAddress.MatchString(p.Address) {
			c.peers[p.Address] = p
		}
	}
	notify := c.notify
	h.mu.Unlock()

	take := func() []relayItem {
		h.mu.Lock()
		defer h.mu.Unlock()
		out := []relayItem{}
		for _, it := range c.outbox {
			if !it.handed {
				it.handed = true
				out = append(out, *it)
			}
		}
		c.seen = time.Now()
		return out
	}
	items := take()
	if len(items) == 0 && req.Wait {
		ctx, cancel := context.WithTimeout(r.Context(), relayPoll)
		defer cancel()
		select {
		case <-notify:
			items = take()
		case <-ctx.Done():
			items = take()
		}
	}
	writeJSON(w, map[string]any{"items": items}, nil)
}

type relayInboxRequest struct {
	ClientID string `json:"client_id"`
	peerMessage
}

// serveRelayInbox：客户端把另一台服务器的 Bot 发来的消息转进来。from 是对方的 relay 地址。
func (bs *Bots) serveRelayInbox(w http.ResponseWriter, r *http.Request) {
	var req relayInboxRequest
	if !decodeJSON(w, r, &req, 64<<10) {
		return
	}
	m := req.peerMessage
	m.Text = strings.TrimSpace(m.Text)
	if !peerMessageID.MatchString(m.ID) || !peerRequestID.MatchString(m.RequestID) ||
		!relayAddress.MatchString(m.From) || !validBotID(m.To) || len(m.Text) == 0 || len(m.Text) > 8000 ||
		(m.ReplyTo != "" && (m.ReplyTo != m.RequestID || m.WantsReply)) {
		writeJSON(w, nil, errors.New("互联消息无效"))
		return
	}
	h := &bs.relay
	h.mu.Lock()
	h.pruneLocked(time.Now())
	c := h.clients[req.ClientID]
	ok := c != nil && c.allowed[m.To]
	if ok {
		_, ok = c.peers[m.From]
	}
	h.mu.Unlock()
	if !ok {
		writeJSON(w, nil, errors.New("这个 Bot 没有打开客户端互联"))
		return
	}
	b, release := bs.acquireBot(m.To)
	defer release()
	if b == nil {
		http.NotFound(w, r)
		return
	}
	if err := bs.acceptRemote(b, m.From, m); err != nil {
		writeJSON(w, nil, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true}, nil)
}

func clip(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
