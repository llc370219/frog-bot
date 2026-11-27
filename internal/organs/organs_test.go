package organs

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"frogbot/internal/clipool"
	"frogbot/internal/host"
	"frogbot/internal/memory"
	"frogbot/internal/vault"
	"github.com/gorilla/websocket"
)

func TestIMAPTwoStageFunnelAndTaintPropagation(t *testing.T) {
	tmp := t.TempDir()
	key, _, _ := vault.LoadKey("", tmp)
	vlt, _ := vault.OpenFile(filepath.Join(tmp, "vault.enc"), key)
	_ = vlt.SetWithRule("qq_imap_auth", "ImapSecret_889900", "QQ IMAP", "imap.qq.com", nil)

	ptr := memory.NewPointerizer(filepath.Join(tmp, "ws"), nil)
	var actorEvents []host.ActorEvent
	var mu sync.Mutex
	doneCh := make(chan struct{}, 2)
	actor := host.NewBotActor("mailbot", 16, func(_ context.Context, ev host.ActorEvent) error {
		mu.Lock()
		actorEvents = append(actorEvents, ev)
		mu.Unlock()
		doneCh <- struct{}{}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actor.Start(ctx)
	defer actor.Close()

	// Build two emails: #1 marketing with List-Unsubscribe (must drop at Stage 1), #2 important long email (pointerize + tainted!)
	marketingMail := "From: promo@shop.example\r\nSubject: Big Sale\r\nList-Unsubscribe: <mailto:unsub@shop.example>\r\n\r\nBuy now!"
	importantMail := "From: boss@company.example\r\nSubject: Q4 Review\r\n\r\n" + strings.Repeat("项目季度复盘重要细节数据。", 150)

	var droppedReasons []string
	funnel := NewIMAPFunnel("mailbot", host.BotIMAPConfig{
		Enabled:         true,
		Host:            "imap.qq.com",
		Port:            993,
		Username:        "user@qq.com",
		PasswordRef:     "{{secret://qq_imap_auth}}",
		FilterMarketing: true,
	}, vlt, ptr, nil, actor)
	funnel.OnDropped = func(_, _, reason string) { droppedReasons = append(droppedReasons, reason) }

	funnel.Dial = func(_ context.Context, _ string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go serveMockIMAP(c2, "ImapSecret_889900", map[string]string{"1": marketingMail, "2": importantMail})
		return c1, nil
	}

	if err := funnel.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected actor event for important email")
	}

	if len(droppedReasons) != 1 || !strings.Contains(droppedReasons[0], "List-Unsubscribe") {
		t.Fatalf("expected 1 marketing email dropped, got %v", droppedReasons)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(actorEvents) != 1 || !actorEvents[0].Tainted || !strings.Contains(actorEvents[0].Text, "已存盘至 tmp_") {
		t.Fatalf("expected 1 tainted pointerized email event, got %+v", actorEvents)
	}
}

func serveMockIMAP(conn net.Conn, expectedPass string, msgs map[string]string) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)
	send := func(s string) {
		_, _ = w.WriteString(s + "\r\n")
		_ = w.Flush()
	}
	send("* OK Mock IMAP Ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			continue
		}
		tag, cmd := parts[0], strings.ToUpper(parts[1])
		switch cmd {
		case "LOGIN":
			if strings.Contains(line, expectedPass) {
				send(tag + " OK Logged in")
			} else {
				send(tag + " NO Bad credentials")
			}
		case "SELECT":
			send("* 2 EXISTS")
			send(tag + " OK Selected")
		case "SEARCH":
			send("* SEARCH 1 2")
			send(tag + " OK Search done")
		case "FETCH":
			rest := strings.Fields(parts[2])
			id := rest[0]
			body := msgs[id]
			send(fmt.Sprintf("* %s FETCH (BODY[] {%d}", id, len(body)))
			_, _ = w.WriteString(body)
			send(")")
			send(tag + " OK Fetch done")
		case "LOGOUT":
			send("* BYE")
			send(tag + " OK Logout")
			return
		}
	}
}

func TestJitterCronAndDigestQueue(t *testing.T) {
	spec, err := ParseJitterCron("0 8 * * * ~15m", "Asia/Shanghai")
	if err != nil {
		t.Fatalf("ParseJitterCron: %v", err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	base := time.Date(2026, 11, 27, 7, 30, 0, 0, loc)
	nom, jit, err := spec.NextFire(base)
	if err != nil {
		t.Fatal(err)
	}
	if nom.Hour() != 8 || nom.Minute() != 0 {
		t.Fatalf("expected nominal 08:00, got %v", nom)
	}
	diff := jit.Sub(nom)
	if diff < 0 || diff >= 15*time.Minute {
		t.Fatalf("jitter out of [0,15m): %v", diff)
	}

	dq, err := OpenDigestQueue(filepath.Join(t.TempDir(), "digest.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = dq.Push(DigestItem{BotID: "checkin", Title: "京东签到", Detail: "成功 +5 京豆"})
	_ = dq.Push(DigestItem{BotID: "checkin", Title: "B站打卡", Detail: "成功"})
	brief, cnt, err := dq.FlushIfDue(time.Date(2026, 11, 27, 8, 30, 0, 0, loc), []string{"08:30", "21:00"}, "Asia/Shanghai")
	if err != nil || cnt != 2 || !strings.Contains(brief, "京东签到") {
		t.Fatalf("DigestQueue flush failed: cnt=%d err=%v brief=%s", cnt, err, brief)
	}
	if len(dq.Pending()) != 0 {
		t.Fatal("expected empty digest queue after flush")
	}
}

func TestBrowserPoolSignalsIdleReapAndReadyToVerifyAutoHeal(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	profile := filepath.Join(tmp, "browser_profile")
	leases := host.NewLeaseManager()
	bp := NewBrowserPool(leases, 150*time.Millisecond)
	defer bp.Close()

	// 1. On-demand run detects WAITING_HUMAN signal and frees RAM immediately (ActiveCount == 0)
	bp.CommandFn = func(ctx context.Context, _ BrowserScriptRequest, _ string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c",
			`echo 'FROG_SIGNAL:WAITING_HUMAN:{"url":"https://checkin.test/login","reason":"需要滑块验证码","screenshot_path":"cap.png"}'`), nil
	}
	out, err := bp.RunScript(context.Background(), BrowserScriptRequest{
		BotID:      "checkin",
		ProfileDir: profile,
		Workspace:  ws,
		ScriptPath: "checkin.mjs",
	})
	if err != nil || out.WaitingHuman == nil || out.WaitingHuman.Reason != "需要滑块验证码" {
		t.Fatalf("expected WAITING_HUMAN signal, got err=%v out=%+v", err, out)
	}
	if bp.ActiveCount() != 0 {
		t.Fatalf("expected 0 active browser procs at rest, got %d", bp.ActiveCount())
	}

	// 2. Ready-to-Verify live CDP handshake over in-process WebSocket pair (net.Pipe)
	qm := clipool.NewQuotaManager([]string{"codex"}, 1, 15)
	qm.AvailabilityCheck = func(string) bool { return true }
	cp := clipool.NewLocalCLIPool(qm, leases, nil, clipool.IsolationOptions{})
	cp.BuildCommand = func(ctx context.Context, _, _, _, _ string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "echo 'fixed selector #btn-checkin'"), nil
	}
	healDone := make(chan clipool.CLITaskCallback, 1)
	cp.OnComplete = func(cb clipool.CLITaskCallback) { healDone <- cb }

	rtv := NewReadyToVerifyManager(bp, cp, leases)
	sess := rtv.RegisterWaitingHuman("checkin", "t1", out.WaitingHuman.URL, "checkin.mjs", profile, ws, out.WaitingHuman.Reason, out.WaitingHuman.ScreenshotPath)

	var cdpMethods []string
	var cdpMu sync.Mutex
	rtv.LaunchCDP = func(ctx context.Context, _ VerifySession) (string, *websocket.Conn, *exec.Cmd, error) {
		clientWS, serverWS := newTestWSPair(t)
		go func() {
			defer serverWS.Close()
			for {
				var msg map[string]any
				if err := serverWS.ReadJSON(&msg); err != nil {
					return
				}
				m, _ := msg["method"].(string)
				cdpMu.Lock()
				cdpMethods = append(cdpMethods, m)
				cdpMu.Unlock()
				if m == "Page.startScreencast" {
					_ = serverWS.WriteJSON(map[string]any{
						"method": "Page.screencastFrame",
						"params": map[string]any{
							"data":      "ZmFrZV9qcGVn",
							"sessionId": 42,
							"metadata":  map[string]int{"deviceWidth": 1280, "deviceHeight": 800},
						},
					})
				}
			}
		}()
		return "", clientWS, nil, nil
	}

	frameCh := make(chan VerifyFrame, 2)
	if err := rtv.StartLiveSession(context.Background(), sess.ID, func(f VerifyFrame) { frameCh <- f }); err != nil {
		t.Fatalf("StartLiveSession: %v", err)
	}
	select {
	case f := <-frameCh:
		if f.DataBase64 != "ZmFrZV9qcGVn" {
			t.Fatalf("unexpected frame: %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CDP screencast frame")
	}
	if err := rtv.DispatchInput(sess.ID, VerifyInputEvent{Type: "drag", X: 0.1, Y: 0.5, EndX: 0.8, EndY: 0.5, Normalized: true, DurationMs: 120}); err != nil {
		t.Fatalf("DispatchInput drag: %v", err)
	}
	if err := rtv.DispatchInput(sess.ID, VerifyInputEvent{Type: "text", Text: "A7B9"}); err != nil {
		t.Fatalf("DispatchInput text: %v", err)
	}
	if _, err := rtv.CompleteSession(sess.ID); err != nil {
		t.Fatalf("CompleteSession: %v", err)
	}

	time.Sleep(40 * time.Millisecond)
	cdpMu.Lock()
	joined := strings.Join(cdpMethods, ",")
	cdpMu.Unlock()
	if !strings.Contains(joined, "Page.screencastFrameAck") || !strings.Contains(joined, "Input.dispatchMouseEvent") || !strings.Contains(joined, "Input.insertText") {
		t.Fatalf("missing expected CDP calls: %s", joined)
	}

	// 3. Website layout redesign Auto-Heal dispatches to LocalCLIPool with PriorityAutoHeal
	if _, err := rtv.TriggerAutoHeal(context.Background(), "checkin", ws, filepath.Join(tmp, "logs"), "", "checkin.mjs", SelectorBrokenSignal{
		URL:      "https://checkin.test",
		Selector: "#old-btn",
		Error:    "Timeout waiting for #old-btn",
	}); err != nil {
		t.Fatalf("TriggerAutoHeal: %v", err)
	}
	select {
	case cb := <-healDone:
		if cb.Status != "completed" || !strings.Contains(cb.Summary, "fixed selector") {
			t.Fatalf("unexpected auto-heal callback: %+v", cb)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for auto-heal CLI callback")
	}
}

// newTestWSPair creates a connected gorilla/websocket client+server pair over an in-memory net.Pipe()
// (no TCP sockets needed inside sandbox).
func newTestWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	ln := newPipeListener()
	t.Cleanup(func() { _ = ln.Close() })
	srvDone := make(chan *websocket.Conn, 1)
	go func() {
		up := websocket.Upgrader{}
		_ = (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := up.Upgrade(w, r, nil)
			if err == nil {
				srvDone <- ws
			}
		})}).Serve(ln)
	}()
	d := websocket.Dialer{
		NetDialContext: func(_ context.Context, _, _ string) (net.Conn, error) { return ln.Dial() },
	}
	clientWS, _, err := d.Dial("ws://pipe.local/cdp", nil)
	if err != nil {
		t.Fatalf("pipe ws dial: %v", err)
	}
	serverWS := <-srvDone
	return clientWS, serverWS
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
