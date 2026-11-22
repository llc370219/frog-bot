// Package agy 直接发现并连接宿主机已安装的各类 Coding Agent CLI（Antigravity、Claude Code、Codex、Pi、Gemini CLI、OpenCode），
// 复用本机登录态与真实项目目录，支持多轮流式输出、思考链、工具调用与权限确认。
package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const maxRPCLine = 32 << 20 // 单条 JSON-RPC 消息上限 32 MiB

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type acpClient struct {
	exe         string
	wrapCmd     func(*exec.Cmd) error
	onUpdate    func(sessionID string, update json.RawMessage)
	onPerm      func(ctx context.Context, req permWireRequest) string // 返回选中的 optionId；空串表示取消
	onDead      func(err error)
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	writeMu     sync.Mutex
	pendingMu   sync.Mutex
	pending     map[int64]chan rpcResult
	nextID      atomic.Int64
	done        chan struct{}
	closeOnce   sync.Once
	agentName   string
	agentVer    string
	stderrTail  bytes.Buffer
	stderrTailM sync.Mutex
}

type rpcResult struct {
	raw json.RawMessage
	err error
}

type permWireRequest struct {
	SessionID string           `json:"sessionId"`
	ToolCall  permWireToolCall `json:"toolCall"`
	Options   []PermOption     `json:"options"`
}

type permWireToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Args       json.RawMessage `json:"args"`
	RawInput   json.RawMessage `json:"rawInput"`
}

// PermOption 是 agy 自身请求用户授权时提供的选项（如 Allow once / Allow always / Reject）。
type PermOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"`
}

func startACPClient(exe string, acpArgs []string, wrapCmd func(*exec.Cmd) error, onUpdate func(string, json.RawMessage), onPerm func(context.Context, permWireRequest) string, onDead func(error)) (*acpClient, error) {
	if len(acpArgs) == 0 {
		acpArgs = []string{"acp"}
	}
	cmd := exec.Command(exe, acpArgs...)
	if wrapCmd != nil {
		if err := wrapCmd(cmd); err != nil {
			return nil, err
		}
	}
	cmd.Env = buildChildEnv(exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动 agy acp：%w", err)
	}
	c := &acpClient{
		exe:      exe,
		wrapCmd:  wrapCmd,
		onUpdate: onUpdate,
		onPerm:   onPerm,
		onDead:   onDead,
		cmd:      cmd,
		stdin:    stdin,
		pending:  map[int64]chan rpcResult{},
		done:     make(chan struct{}),
	}
	go c.captureStderr(stderr)
	go c.readLoop(stdout)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var initRes struct {
		AgentInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "frog-bot", "version": "1.0"},
	}, &initRes); err != nil {
		c.Close()
		return nil, fmt.Errorf("初始化 agy acp 失败：%w", err)
	}
	c.agentName = initRes.AgentInfo.Name
	c.agentVer = initRes.AgentInfo.Version
	return c, nil
}

func cutString(s string, sep byte) (string, string, bool) {
	if i := bytes.IndexByte([]byte(s), sep); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

func (c *acpClient) captureStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			c.stderrTailM.Lock()
			if c.stderrTail.Len()+n > 8192 {
				c.stderrTail.Reset()
			}
			_, _ = c.stderrTail.Write(buf[:n])
			c.stderrTailM.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (c *acpClient) lastStderr() string {
	c.stderrTailM.Lock()
	defer c.stderrTailM.Unlock()
	return c.stderrTail.String()
}

func (c *acpClient) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 64*1024)
	var readErr error
	for {
		line, err := readLineLimited(br, maxRPCLine)
		if len(line) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	_ = c.cmd.Wait()
	close(c.done)
	if readErr == nil {
		if tail := c.lastStderr(); tail != "" {
			readErr = fmt.Errorf("agy acp 已退出：%s", truncateStr(tail, 400))
		} else {
			readErr = errors.New("agy acp 进程已退出")
		}
	}
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		ch <- rpcResult{err: readErr}
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	if c.onDead != nil {
		c.onDead(readErr)
	}
}

func readLineLimited(r *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		frag, isPrefix, err := r.ReadLine()
		buf = append(buf, frag...)
		if len(buf) > limit {
			return nil, errors.New("agy acp 单条消息超过 32 MiB")
		}
		if !isPrefix || err != nil {
			return buf, err
		}
	}
}

func (c *acpClient) dispatch(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var env rpcEnvelope
	if json.Unmarshal(line, &env) != nil {
		return
	}
	// 1) Server -> Client 请求（如 session/request_permission）
	if env.Method != "" && len(env.ID) > 0 && string(env.ID) != "null" {
		go c.handleServerRequest(env)
		return
	}
	// 2) Server -> Client 通知（如 session/update）
	if env.Method != "" {
		if env.Method == "session/update" && c.onUpdate != nil {
			var p struct {
				SessionID string          `json:"sessionId"`
				Update    json.RawMessage `json:"update"`
			}
			if json.Unmarshal(env.Params, &p) == nil && p.SessionID != "" {
				c.onUpdate(p.SessionID, p.Update)
			}
		}
		return
	}
	// 3) Client -> Server 请求的响应
	if len(env.ID) > 0 {
		var id int64
		if json.Unmarshal(env.ID, &id) != nil {
			return
		}
		c.pendingMu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.pendingMu.Unlock()
		if ch != nil {
			if env.Error != nil {
				ch <- rpcResult{err: env.Error}
			} else {
				ch <- rpcResult{raw: env.Result}
			}
		}
	}
}

func (c *acpClient) handleServerRequest(env rpcEnvelope) {
	if env.Method != "session/request_permission" {
		_ = c.writeRaw(map[string]any{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"error":   map[string]any{"code": -32601, "message": "method not supported"},
		})
		return
	}
	var req permWireRequest
	if err := json.Unmarshal(env.Params, &req); err != nil {
		_ = c.writeRaw(map[string]any{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"result":  map[string]any{"outcome": map[string]any{"outcome": "cancelled"}},
		})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	chosen := ""
	if c.onPerm != nil {
		chosen = c.onPerm(ctx, req)
	}
	cancel()
	var outcome map[string]any
	if chosen == "" {
		outcome = map[string]any{"outcome": "cancelled"}
	} else {
		outcome = map[string]any{"outcome": "selected", "optionId": chosen}
	}
	_ = c.writeRaw(map[string]any{
		"jsonrpc": "2.0",
		"id":      env.ID,
		"result":  map[string]any{"outcome": outcome},
	})
}

func (c *acpClient) writeRaw(msg any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(raw)
	return err
}

func (c *acpClient) notify(method string, params any) error {
	return c.writeRaw(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *acpClient) call(ctx context.Context, method string, params any, out any) error {
	select {
	case <-c.done:
		return errors.New("agy acp 连接已关闭")
	default:
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcResult, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	if err := c.writeRaw(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return err
	}
	select {
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if out != nil && len(res.raw) > 0 {
			return json.Unmarshal(res.raw, out)
		}
		return nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return ctx.Err()
	case <-c.done:
		return errors.New("agy acp 进程已断开")
	}
}

func (c *acpClient) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *acpClient) Close() {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		// 先关闭 stdin 给 agy 留一点优雅落盘会话状态的时间，超时再强制结束进程。
		select {
		case <-c.done:
			return
		case <-time.After(1500 * time.Millisecond):
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		select {
		case <-c.done:
		case <-time.After(500 * time.Millisecond):
		}
	})
}

// ResolveBinary 查找本机上的 agy 可执行文件（保持向后兼容）。
func ResolveBinary() (string, error) {
	return ResolveAgentBinary("agy")
}

func isExecFile(p string) bool {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return st.Mode().Perm()&0o111 != 0
}

func truncateStr(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "…"
}
