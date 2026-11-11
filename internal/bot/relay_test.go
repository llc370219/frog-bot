package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"frogbot/internal/onefrog"
)

// relayTestClient 模拟 Mac / 安卓客户端：同时连着两台服务器，在它们之间转发文字消息。
type relayTestClient struct {
	t       *testing.T
	id      string
	servers map[string]*httptest.Server // 服务器 key → 地址
	mu      sync.Mutex
	enabled bool
}

func (c *relayTestClient) post(ctx context.Context, srv *httptest.Server, path string, body any, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var v map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return err
	}
	if e, ok := v["error"]; ok {
		var s string
		_ = json.Unmarshal(e, &s)
		return &relayErr{s}
	}
	if out != nil {
		raw, _ := json.Marshal(v)
		return json.Unmarshal(raw, out)
	}
	return nil
}

type relayErr struct{ s string }

func (e *relayErr) Error() string { return e.s }

// loop 对一台服务器长轮询：取走待转发消息，送到目标服务器，下一轮带上回执。
func (c *relayTestClient) loop(ctx context.Context, key string) {
	srv := c.servers[key]
	type ack struct {
		ID    string `json:"id"`
		Error string `json:"error,omitempty"`
	}
	var acks []ack
	for ctx.Err() == nil {
		c.mu.Lock()
		enabled := c.enabled
		c.mu.Unlock()
		var peers []relayPeer
		for k := range c.servers {
			if k != key {
				peers = append(peers, relayPeer{Address: "relay:" + k + "/main", Name: "默认 Bot", Server: k})
			}
		}
		var out struct {
			Items []relayItem `json:"items"`
		}
		body := map[string]any{"client_id": c.id, "enabled": enabled, "allowed": []string{"main"}, "peers": peers, "acks": acks, "wait": true}
		if err := c.post(ctx, srv, "/api/relay/sync", body, &out); err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		acks = nil
		for _, it := range out.Items {
			target := strings.TrimPrefix(it.To, "relay:")
			k, bot, _ := strings.Cut(target, "/")
			msg := map[string]any{"client_id": c.id, "id": it.ID, "from": "relay:" + key + "/" + it.From, "to": bot,
				"text": it.Text, "request_id": it.RequestID, "reply_to": it.ReplyTo, "wants_reply": it.WantsReply}
			a := ack{ID: it.ID}
			if err := c.post(ctx, c.servers[k], "/api/relay/inbox", msg, nil); err != nil {
				a.Error = err.Error()
			}
			acks = append(acks, a)
		}
		if !enabled {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestClientRelayAskAndReply(t *testing.T) {
	ask := regexp.MustCompile(`来自 Bot (relay:[a-z0-9]+/main) 的消息；request_id=(req_[a-f0-9]{16})；reply_to=；需要回复=true`)
	model := peerTestModel(func(ctx context.Context, req onefrog.ChatRequest, delta func(string)) (onefrog.Message, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "user" {
			if m := ask.FindStringSubmatch(last.Content); len(m) > 0 {
				return toolCall("message_agent", map[string]string{"to_bot_id": m[1], "reply_to": m[2], "message": "我记得：答案是 42"}), nil
			}
			if strings.Contains(last.Content, "去问问") {
				return toolCall("ask_agent", map[string]string{"to_bot_id": "relay:beta/main", "question": "你记得答案吗？"}), nil
			}
		}
		return onefrog.Message{Content: "好的"}, nil
	})
	open := func(name string) (*Bots, *httptest.Server) {
		dir := t.TempDir()
		bs, err := OpenBots(Config{Name: name, DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
			ChatProvider: model, ChatModel: "chat"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(bs.Close)
		srv := httptest.NewServer(LocalOnly(bs.Handler(nil)))
		t.Cleanup(srv.Close)
		return bs, srv
	}
	alpha, alphaSrv := open("alpha")
	beta, betaSrv := open("beta")

	// 客户端没开：通讯录里没有对方，发消息直接报错
	if strings.Contains(alpha.main.listAgents(), "relay:") {
		t.Fatal("relay peers listed without a client")
	}
	if _, _, err := alpha.deliverRelay("main", "relay:beta/main", "在吗", "", true); err == nil {
		t.Fatal("relay delivery must fail without a client")
	}

	client := &relayTestClient{t: t, id: "test-client-0001", enabled: true,
		servers: map[string]*httptest.Server{"alpha": alphaSrv, "beta": betaSrv}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // 先于 httptest.Server.Close 执行，长轮询请求会被取消
	go client.loop(ctx, "alpha")
	go client.loop(ctx, "beta")
	waitFor(t, "relay directory", func() bool {
		return strings.Contains(alpha.main.listAgents(), "relay:beta/main") && strings.Contains(beta.main.listAgents(), "relay:alpha/main")
	})

	if _, err := alpha.main.Send("去问问 beta 记不记得答案"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply relayed back", func() bool {
		for _, m := range alpha.main.store.Inbox(true) {
			if m.FromBotID == "relay:beta/main" && m.ReplyTo != "" && strings.Contains(m.Text, "答案是 42") {
				return true
			}
		}
		return false
	})
	// 对方收到的是普通收件箱消息，来源是 relay 地址
	found := false
	for _, m := range beta.main.store.Inbox(true) {
		if m.FromBotID == "relay:alpha/main" && m.WantsReply && strings.Contains(m.Text, "你记得答案吗") {
			found = true
		}
	}
	if !found {
		t.Fatal("beta did not receive the relayed question")
	}

	// 未加入互联的 Bot 不能借客户端收消息
	raw, _ := json.Marshal(map[string]any{"client_id": "test-client-0001", "id": onefrog.NewID("msg"), "from": "relay:alpha/main",
		"to": "bot_0123456789abcdef", "text": "hi", "request_id": onefrog.NewID("req")})
	resp, err := http.Post(betaSrv.URL+"/api/relay/inbox", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if v["error"] == "" {
		t.Fatal("relay inbox accepted a bot that is not opted in")
	}

	// 关掉总开关：客户端马上告诉每台服务器，对方立刻从通讯录消失，再发就报错
	client.mu.Lock()
	client.enabled = false
	client.mu.Unlock()
	for _, srv := range client.servers {
		if err := client.post(ctx, srv, "/api/relay/sync", map[string]any{"client_id": client.id, "enabled": false}, nil); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "relay directory cleared", func() bool {
		return !strings.Contains(alpha.main.listAgents(), "relay:")
	})
	if _, _, err := alpha.deliverRelay("main", "relay:beta/main", "还在吗", "", false); err == nil {
		t.Fatal("relay delivery must fail after the client disables relay")
	}
}
