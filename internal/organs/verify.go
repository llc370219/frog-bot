package organs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"frogbot/internal/clipool"
	"frogbot/internal/host"
	"github.com/gorilla/websocket"
)

// VerifySession tracks a Ready-to-Verify human captcha/login checkpoint.
// While Status == "WAITING_HUMAN", ZERO Chromium RAM is held; browser only cold-starts
// when the user clicks "我现在有空，开始验证".
type VerifySession struct {
	ID             string    `json:"id"`
	BotID          string    `json:"bot_id"`
	TaskID         string    `json:"task_id,omitempty"`
	URL            string    `json:"url"`
	ScriptPath     string    `json:"script_path,omitempty"`
	ProfileDir     string    `json:"profile_dir,omitempty"`
	Workspace      string    `json:"workspace,omitempty"`
	Reason         string    `json:"reason"`
	ScreenshotPath string    `json:"screenshot_path,omitempty"`
	Status         string    `json:"status"` // WAITING_HUMAN | VERIFYING | COMPLETED | CANCELLED
	ViewportWidth  int       `json:"viewport_width"`
	ViewportHeight int       `json:"viewport_height"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// VerifyFrame is a live CDP Page.screencastFrame JPEG payload pushed to Mac/Android/Web Canvas.
type VerifyFrame struct {
	SessionID string `json:"session_id"`
	DataBase64 string `json:"data_base64"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Seq       int64  `json:"seq"`
}

// VerifyInputEvent represents reverse touch/mouse/slider/keyboard input from client canvas.
// Normalized coordinates (0..1) or pixel coordinates are automatically scaled to CDP viewport.
type VerifyInputEvent struct {
	Type       string  `json:"type"` // "click" | "drag" | "text" | "key" | "scroll"
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	EndX       float64 `json:"end_x,omitempty"`
	EndY       float64 `json:"end_y,omitempty"`
	Normalized bool    `json:"normalized,omitempty"` // true -> X,Y in [0,1]
	Text       string  `json:"text,omitempty"`
	Key        string  `json:"key,omitempty"`
	DeltaY     float64 `json:"delta_y,omitempty"`
	DurationMs int     `json:"duration_ms,omitempty"`
}

// CDPLauncher launches a browser reaching the verification screen and returns its CDP WebSocket URL
// (or a custom *websocket.Conn in tests) plus a cleanup function.
type CDPLauncher func(ctx context.Context, sess VerifySession) (wsURL string, conn *websocket.Conn, proc *exec.Cmd, err error)

type liveVerifyConn struct {
	ws     *websocket.Conn
	mu     sync.Mutex
	cmdSeq atomic.Int64
	cancel context.CancelFunc
	lease  *host.Lease
	procID string
}

func (l *liveVerifyConn) sendCDP(method string, params any) error {
	id := l.cmdSeq.Add(1)
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ws.WriteJSON(msg)
}

// ReadyToVerifyManager implements the never-expiring Ready-to-Verify captcha handshake
// and automatic website selector-change self-healing via LocalCLIPool.
type ReadyToVerifyManager struct {
	BrowserPool   *BrowserPool
	CLIPool       *clipool.LocalCLIPool
	Leases        *host.LeaseManager
	LaunchCDP     CDPLauncher
	OnNotifyHuman func(sess VerifySession)
	OnFrame       func(frame VerifyFrame)
	OnCompleted   func(sess VerifySession)

	mu       sync.RWMutex
	sessions map[string]*VerifySession
	live     map[string]*liveVerifyConn
}

func NewReadyToVerifyManager(bp *BrowserPool, cp *clipool.LocalCLIPool, leases *host.LeaseManager) *ReadyToVerifyManager {
	if leases == nil {
		leases = host.NewLeaseManager()
	}
	m := &ReadyToVerifyManager{
		BrowserPool: bp,
		CLIPool:     cp,
		Leases:      leases,
		sessions:    make(map[string]*VerifySession),
		live:        make(map[string]*liveVerifyConn),
	}
	m.LaunchCDP = defaultLaunchCDP
	return m
}

// RegisterWaitingHuman records a WAITING_HUMAN checkpoint after the background browser
// has ALREADY exited (0 MB RAM), and fires an offline push notification to the user.
func (m *ReadyToVerifyManager) RegisterWaitingHuman(botID, taskID, url, scriptPath, profileDir, workspace, reason, screenshotPath string) VerifySession {
	var rb [6]byte
	_, _ = rand.Read(rb[:])
	now := time.Now()
	s := &VerifySession{
		ID:             "ver_" + hex.EncodeToString(rb[:]),
		BotID:          botID,
		TaskID:         taskID,
		URL:            url,
		ScriptPath:     scriptPath,
		ProfileDir:     profileDir,
		Workspace:      workspace,
		Reason:         reason,
		ScreenshotPath: screenshotPath,
		Status:         "WAITING_HUMAN",
		ViewportWidth:  1280,
		ViewportHeight: 800,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	if m.OnNotifyHuman != nil {
		m.OnNotifyHuman(*s)
	}
	return *s
}

// List returns all verification sessions (optionally filtered by status).
func (m *ReadyToVerifyManager) List(status string) []VerifySession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []VerifySession
	for _, s := range m.sessions {
		if status != "" && s.Status != status {
			continue
		}
		out = append(out, *s)
	}
	return out
}

// StartLiveSession implements the user clicking "我现在有空，开始验证":
// cold-starts the browser with browser_profile/, connects to CDP, and starts Page.startScreencast.
func (m *ReadyToVerifyManager) StartLiveSession(parentCtx context.Context, sessionID string, onFrame func(VerifyFrame)) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return errors.New("验证会话不存在")
	}
	sessCopy := *sess
	m.mu.Unlock()

	lease, err := m.Leases.Acquire(parentCtx, host.BrowserProfileResource(sessCopy.BotID), "verify:"+sessionID, 10*time.Minute)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parentCtx)
	wsURL, conn, proc, err := m.LaunchCDP(ctx, sessCopy)
	if err != nil {
		cancel()
		lease.Release()
		return fmt.Errorf("启动实时验证浏览器失败: %w", err)
	}
	if conn == nil {
		dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		conn, _, err = dialer.DialContext(ctx, wsURL, nil)
		if err != nil {
			cancel()
			if proc != nil && proc.Process != nil {
				_ = proc.Process.Kill()
			}
			lease.Release()
			return fmt.Errorf("连接 CDP WebSocket 失败: %w", err)
		}
	}

	procID := "verify_" + sessionID
	if m.BrowserPool != nil && proc != nil {
		m.BrowserPool.RegisterLongLivedProcess(procID, sessCopy.BotID, proc, lease, cancel)
	}

	lc := &liveVerifyConn{ws: conn, cancel: cancel, lease: lease, procID: procID}
	m.mu.Lock()
	sess.Status = "VERIFYING"
	sess.UpdatedAt = time.Now()
	m.live[sessionID] = lc
	m.mu.Unlock()

	_ = lc.sendCDP("Page.enable", nil)
	if sessCopy.URL != "" {
		_ = lc.sendCDP("Page.navigate", map[string]any{"url": sessCopy.URL})
	}
	if err := lc.sendCDP("Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       70,
		"maxWidth":      sessCopy.ViewportWidth,
		"maxHeight":     sessCopy.ViewportHeight,
		"everyNthFrame": 1,
	}); err != nil {
		m.StopLiveSession(sessionID, "CANCELLED")
		return err
	}

	go m.readCDPFrames(ctx, sessionID, lc, onFrame)
	return nil
}

func (m *ReadyToVerifyManager) readCDPFrames(ctx context.Context, sessionID string, lc *liveVerifyConn, onFrame func(VerifyFrame)) {
	var seq int64
	for ctx.Err() == nil {
		var msg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := lc.ws.ReadJSON(&msg); err != nil {
			return
		}
		if msg.Method != "Page.screencastFrame" {
			continue
		}
		var p struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
			Metadata  struct {
				DeviceWidth  int `json:"deviceWidth"`
				DeviceHeight int `json:"deviceHeight"`
			} `json:"metadata"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			continue
		}
		// Immediately ack CDP frame so Chromium keeps streaming smoothly
		_ = lc.sendCDP("Page.screencastFrameAck", map[string]any{"sessionId": p.SessionID})
		if m.BrowserPool != nil {
			m.BrowserPool.TouchProcess(lc.procID)
		}
		seq++
		frame := VerifyFrame{
			SessionID:  sessionID,
			DataBase64: p.Data,
			Width:      p.Metadata.DeviceWidth,
			Height:     p.Metadata.DeviceHeight,
			Seq:        seq,
		}
		if onFrame != nil {
			onFrame(frame)
		} else if m.OnFrame != nil {
			m.OnFrame(frame)
		}
	}
}

// DispatchInput translates client touch/mouse/slider/keyboard input into CDP Input commands,
// including human-like smooth curved drag trajectories for slider captchas.
func (m *ReadyToVerifyManager) DispatchInput(sessionID string, ev VerifyInputEvent) error {
	m.mu.RLock()
	lc := m.live[sessionID]
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if lc == nil || sess == nil {
		return errors.New("当前没有进行中的验证画面推流")
	}
	if m.BrowserPool != nil {
		m.BrowserPool.TouchProcess(lc.procID)
	}

	scale := func(x, y float64) (float64, float64) {
		if !ev.Normalized {
			return x, y
		}
		return x * float64(sess.ViewportWidth), y * float64(sess.ViewportHeight)
	}

	switch strings.ToLower(ev.Type) {
	case "click":
		x, y := scale(ev.X, ev.Y)
		if err := lc.sendCDP("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1}); err != nil {
			return err
		}
		return lc.sendCDP("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1})

	case "drag":
		x0, y0 := scale(ev.X, ev.Y)
		x1, y1 := scale(ev.EndX, ev.EndY)
		steps := 12
		if ev.DurationMs > 0 && ev.DurationMs < 2000 {
			steps = max(8, ev.DurationMs/25)
		}
		if err := lc.sendCDP("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": x0, "y": y0, "button": "left", "clickCount": 1}); err != nil {
			return err
		}
		// Ease-out human-like trajectory with slight natural vertical micro-jitter
		for i := 1; i <= steps; i++ {
			t := float64(i) / float64(steps)
			eased := 1 - math.Pow(1-t, 2.2)
			cx := x0 + (x1-x0)*eased
			cy := y0 + (y1-y0)*eased + math.Sin(t*math.Pi)*1.5
			if err := lc.sendCDP("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": cx, "y": cy, "button": "left"}); err != nil {
				return err
			}
			time.Sleep(12 * time.Millisecond)
		}
		return lc.sendCDP("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": x1, "y": y1, "button": "left", "clickCount": 1})

	case "text":
		return lc.sendCDP("Input.insertText", map[string]any{"text": ev.Text})

	case "key":
		if err := lc.sendCDP("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": ev.Key}); err != nil {
			return err
		}
		return lc.sendCDP("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": ev.Key})

	case "scroll":
		x, y := scale(ev.X, ev.Y)
		return lc.sendCDP("Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": x, "y": y, "deltaX": 0, "deltaY": ev.DeltaY})

	default:
		return fmt.Errorf("不支持的验证输入类型: %s", ev.Type)
	}
}

// CompleteSession finishes remote verification, flushes browser_profile/ to disk, closes
// Chromium to free RAM, and fires OnCompleted to resume the background check-in task.
func (m *ReadyToVerifyManager) CompleteSession(sessionID string) (VerifySession, error) {
	sess, ok := m.StopLiveSession(sessionID, "COMPLETED")
	if !ok {
		return VerifySession{}, errors.New("验证会话不存在")
	}
	if m.OnCompleted != nil {
		m.OnCompleted(sess)
	}
	return sess, nil
}

// StopLiveSession tears down CDP screencast & browser process cleanly.
func (m *ReadyToVerifyManager) StopLiveSession(sessionID, finalStatus string) (VerifySession, bool) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	lc := m.live[sessionID]
	delete(m.live, sessionID)
	var out VerifySession
	if ok {
		sess.Status = finalStatus
		sess.UpdatedAt = time.Now()
		out = *sess
	}
	m.mu.Unlock()

	if lc != nil {
		_ = lc.sendCDP("Page.stopScreencast", nil)
		_ = lc.ws.Close()
		lc.cancel()
		if m.BrowserPool != nil {
			m.BrowserPool.StopProcess(lc.procID)
		}
		if lc.lease != nil {
			lc.lease.Release()
		}
	}
	return out, ok
}

// TriggerAutoHeal dispatches a PriorityAutoHeal job to LocalCLIPool when a check-in script
// breaks due to a website DOM/selector redesign, attaching error log, screenshot, and pruned DOM.
func (m *ReadyToVerifyManager) TriggerAutoHeal(ctx context.Context, botID, workspace, logsDir, execSock, scriptPath string, sig SelectorBrokenSignal) (clipool.CLITaskState, error) {
	if m.CLIPool == nil {
		return clipool.CLITaskState{}, errors.New("LocalCLIPool 未初始化")
	}
	prompt := fmt.Sprintf(
		"网站页面疑似改版导致自动化脚本 %s 选择器报错，请自动修复并验证：\n"+
			"- 目标 URL: %s\n- 失效选择器: %s\n- 错误日志: %s\n- 留证截图: %s\n- 精简 DOM 片段:\n```html\n%s\n```\n"+
			"请修改脚本修复选择器，使用 frog-exec 做安全自测确认通过，并在最后总结修改点以便更新 SKILL.md。",
		scriptPath, sig.URL, sig.Selector, sig.Error, sig.ScreenshotPath, sig.DOMSnippet,
	)
	return m.CLIPool.Dispatch(ctx, clipool.CLITaskRequest{
		BotID:          botID,
		Mode:           "auto",
		TaskPrompt:     prompt,
		Workspace:      workspace,
		LogsDir:        logsDir,
		Priority:       clipool.PriorityAutoHeal,
		ExecSocketPath: execSock,
	})
}

func defaultLaunchCDP(ctx context.Context, sess VerifySession) (string, *websocket.Conn, *exec.Cmd, error) {
	var bin string
	for _, cand := range []string{"chromium", "chromium-browser", "google-chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if p, err := exec.LookPath(cand); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		return "", nil, nil, errors.New("未找到 Chromium/Chrome 可执行文件，请运行 Host Doctor 安装 Playwright Chromium")
	}
	port := 19222
	args := []string{
		"--headless=new",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--no-first-run",
		"--no-default-browser-check",
		fmt.Sprintf("--window-size=%d,%d", sess.ViewportWidth, sess.ViewportHeight),
	}
	if sess.ProfileDir != "" {
		args = append(args, "--user-data-dir="+sess.ProfileDir)
	}
	if sess.URL != "" {
		args = append(args, sess.URL)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if err := cmd.Start(); err != nil {
		return "", nil, nil, err
	}

	// Poll /json/list for up to 5s to discover the page WebSocket debugger URL
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
		if err == nil {
			var targets []struct {
				Type                 string `json:"type"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&targets)
			resp.Body.Close()
			for _, t := range targets {
				if t.Type == "page" && t.WebSocketDebuggerURL != "" {
					return t.WebSocketDebuggerURL, nil, cmd, nil
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return "", nil, nil, errors.New("等待 Chromium CDP 端点就绪超时")
}
