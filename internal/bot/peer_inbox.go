package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"frogbot/internal/onefrog"
)

type peerMessage struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Text       string `json:"text"`
	RequestID  string `json:"request_id"`
	ReplyTo    string `json:"reply_to,omitempty"`
	WantsReply bool   `json:"wants_reply,omitempty"`
}

var peerMessageID = regexp.MustCompile(`^msg_[0-9a-f]{16}$`)
var peerRequestID = regexp.MustCompile(`^req_[0-9a-f]{16}$`)

func (bs *Bots) deliverPeer(from, address, text, replyTo string, wantsReply bool) (string, string, error) {
	device, to, _ := strings.Cut(address, "/")
	peer, ok := bs.network.find(device)
	if !ok || !validBotID(to) {
		return "", "", errors.New("远程 Bot 地址无效，请先用 list_agents 查看")
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
	w := peerMessage{ID: onefrog.NewID("msg"), From: from, To: to, Text: text,
		RequestID: onefrog.NewID("req"), WantsReply: wantsReply}
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
			return "", "", errors.New("reply_to 未匹配到该远程 Bot 发来的待处理请求")
		}
		w.RequestID, w.ReplyTo, w.WantsReply = replyTo, replyTo, false
	}
	hop := 1
	if replyTo != "" {
		hop = 2
	}
	// Store the outgoing request before sending it: a fast peer may reply
	// before the HTTP acknowledgement reaches this device.
	b.store.Append("message", Message{ID: w.ID, Role: "notice", Text: w.Text, FromBotID: from,
		ToBotID: address, RequestID: w.RequestID, ReplyTo: w.ReplyTo, WantsReply: w.WantsReply,
		MessageState: "sent", Hop: hop, Silent: true, CreatedAt: time.Now()}, true)
	raw, _ := json.Marshal(w)
	ctx, cancel := context.WithTimeout(b.ctx, 6*time.Second)
	defer cancel()
	resp, err := bs.network.request(ctx, peer, "POST", "/inbox", bytes.NewReader(raw), "")
	if err != nil {
		bs.deliveryMu.Lock()
		if sent, exists := b.store.Message(w.ID); exists && sent.MessageState == "sent" {
			b.store.Append("message.state", map[string]string{"ID": w.ID, "State": "delivery_failed"}, true)
		}
		bs.deliveryMu.Unlock()
		return "", "", err
	}
	resp.Body.Close()
	if original.ID != "" {
		b.store.Append("message.state", map[string]string{"ID": original.ID, "State": "handled"}, true)
	}
	return w.ID, w.RequestID, nil
}

func (bs *Bots) receivePeerMessage(w http.ResponseWriter, r *http.Request) {
	var m peerMessage
	if err := readPeerJSON(w, r, &m); err != nil {
		writeJSON(w, nil, err)
		return
	}
	m.Text = strings.TrimSpace(m.Text)
	if !peerMessageID.MatchString(m.ID) || !peerRequestID.MatchString(m.RequestID) ||
		!validBotID(m.From) || !validBotID(m.To) || len(m.Text) == 0 || len(m.Text) > 8000 ||
		(m.ReplyTo != "" && (m.ReplyTo != m.RequestID || m.WantsReply)) {
		writeJSON(w, nil, errors.New("设备收件箱消息无效"))
		return
	}
	b, release := bs.acquireBot(m.To)
	defer release()
	if b == nil {
		http.NotFound(w, r)
		return
	}
	from := r.Header.Get(deviceHeader) + "/" + m.From
	if err := bs.acceptRemote(b, from, m); err != nil {
		writeJSON(w, nil, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true}, nil)
}

// acceptRemote 把另一台设备（或客户端互联转来）的消息写进收件箱并唤醒聊天层。
// from 是对方在本机看到的地址（设备ID/BotID 或 relay:服务器/BotID）；同一条消息重复送达时直接算成功。
func (bs *Bots) acceptRemote(b *Bot, from string, m peerMessage) error {
	bs.deliveryMu.Lock()
	defer bs.deliveryMu.Unlock()
	if existing, ok := b.store.Message(m.ID); ok {
		if existing.FromBotID == from && existing.RequestID == m.RequestID && existing.ToBotID == m.To {
			return nil
		}
		return errors.New("消息 ID 冲突")
	}
	hop := 1
	if m.ReplyTo != "" {
		var original string
		for _, sent := range b.store.Inbox(true) {
			if sent.FromBotID == m.To && sent.ToBotID == from && sent.RequestID == m.ReplyTo &&
				sent.WantsReply && (sent.MessageState == "sent" || sent.MessageState == "delivery_failed") {
				original = sent.ID
				break
			}
		}
		if original == "" {
			return errors.New("找不到与这条回复匹配的请求")
		}
		hop = 2
		b.store.Append("message.state", map[string]string{"ID": original, "State": "replied"}, true)
	}
	b.store.Append("message", Message{ID: m.ID, Role: "notice", Text: m.Text, FromBotID: from, ToBotID: m.To,
		RequestID: m.RequestID, ReplyTo: m.ReplyTo, WantsReply: m.WantsReply, Hop: hop,
		MessageState: "pending", CreatedAt: time.Now()}, true)
	b.wake()
	return nil
}

func (b *Bot) listAgents() string {
	if b.coordinator == nil {
		return "未启用多 Bot"
	}
	var text strings.Builder
	for _, group := range b.coordinator.deviceGroups(b.ctx) {
		fmt.Fprintf(&text, "%s", group.Name)
		if !group.Online {
			text.WriteString("（离线）")
		}
		text.WriteString("\n")
		for _, p := range group.Bots {
			id := p.ID
			if group.ID != "local" {
				id = group.ID + "/" + id
			} else if id == b.ID() {
				continue
			}
			fmt.Fprintf(&text, "- %s: %s\n", id, p.Name)
		}
	}
	if peers := b.coordinator.relay.peersFor(b.ID()); len(peers) > 0 {
		text.WriteString("客户端互联（只在用户的客户端开着时可用，只能文字交流）\n")
		for _, p := range peers {
			fmt.Fprintf(&text, "- %s: %s（服务器：%s）\n", p.Address, p.Name, p.Server)
		}
	}
	return strings.TrimSpace(text.String())
}
