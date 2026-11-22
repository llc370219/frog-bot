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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================================
// 1. Claude Code 驱动（双向 stream-json + stdio 交互式权限卡片 + 会话续接）
// ============================================================================

type claudeSessionRunner struct {
	exe       string
	cwd       string
	mode      string
	model     string
	nativeID  string
	wrapCmd   func(*exec.Cmd) error
	onDelta   func(kind, text string)
	onTool    func(tc ToolCall)
	onPerm    func(ctx context.Context, req permWireRequest) string
	onUsage   func(used, size int64)
	onCommands func(cmds []CommandOption)
	onNativeID func(id string)

	mu       sync.Mutex
	writeMu  sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	turnDone chan error
	streamed bool
	closed   bool
}

func (r *claudeSessionRunner) ensureStarted() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("会话已关闭")
	}
	if r.cmd != nil && r.stdin != nil {
		return nil
	}

	args := []string{
		"-p",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
	}
	switch r.mode {
	case "", "bypassPermissions", "auto":
		args = append(args, "--dangerously-skip-permissions")
	default:
		args = append(args, "--permission-mode", r.mode, "--permission-prompt-tool", "stdio")
	}
	if r.model != "" && r.model != "default" {
		args = append(args, "--model", r.model)
	}
	if r.nativeID != "" {
		args = append(args, "--resume", r.nativeID)
	}

	cmd := exec.Command(r.exe, args...)
	cmd.Dir = r.cwd
	if r.wrapCmd != nil {
		if err := r.wrapCmd(cmd); err != nil {
			return err
		}
	}
	cmd.Env = buildChildEnv(r.exe)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法启动 Claude Code：%w", err)
	}
	r.cmd = cmd
	r.stdin = stdin

	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(&stderrBuf, io.LimitReader(stderr, 16384))
	}()
	go r.readLoop(cmd, stdout, stderrDone, &stderrBuf)
	return nil
}

func (r *claudeSessionRunner) writeStdin(line []byte) error {
	r.mu.Lock()
	stdin := r.stdin
	r.mu.Unlock()
	if stdin == nil {
		return errors.New("Claude Code 输入流不可用")
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_, err := stdin.Write(line)
	return err
}

func (r *claudeSessionRunner) RunTurn(ctx context.Context, prompt string) (string, error) {
	if err := r.ensureStarted(); err != nil {
		return "", err
	}
	doneCh := make(chan error, 1)
	r.mu.Lock()
	r.turnDone = doneCh
	r.streamed = false
	r.mu.Unlock()

	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]string{
				{"type": "text", "text": prompt},
			},
		},
	}
	raw, _ := json.Marshal(msg)
	raw = append(raw, '\n')
	if err := r.writeStdin(raw); err != nil {
		r.Kill()
		return "", fmt.Errorf("发送指令给 Claude Code 失败：%w", err)
	}

	select {
	case err := <-doneCh:
		if err != nil {
			return "", err
		}
		return "end_turn", nil
	case <-ctx.Done():
		r.Interrupt()
		return "cancelled", ctx.Err()
	}
}

func (r *claudeSessionRunner) Interrupt() {
	req, _ := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": newShortID("int"),
		"request":    map[string]string{"subtype": "interrupt"},
	})
	_ = r.writeStdin(append(req, '\n'))
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}
}

func (r *claudeSessionRunner) Reconfigure(mode, model string) {
	r.mu.Lock()
	changed := (mode != "" && mode != r.mode) || (model != "" && model != r.model)
	if mode != "" {
		r.mode = mode
	}
	if model != "" {
		r.model = model
	}
	r.mu.Unlock()
	if changed {
		// 下次发消息时按新参数自动用 --resume 无缝拉起
		r.Kill()
	}
}

func (r *claudeSessionRunner) Kill() {
	r.mu.Lock()
	stdin := r.stdin
	cmd := r.cmd
	r.stdin = nil
	r.cmd = nil
	r.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (r *claudeSessionRunner) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.Kill()
}

func (r *claudeSessionRunner) readLoop(cmd *exec.Cmd, stdout io.Reader, stderrDone <-chan struct{}, stderrBuf *bytes.Buffer) {
	br := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := readLineLimited(br, maxRPCLine)
		if len(line) > 0 {
			r.handleLine(line)
		}
		if err != nil {
			break
		}
	}
	<-stderrDone
	_ = cmd.Wait()
	tail := strings.TrimSpace(stderrBuf.String())

	r.mu.Lock()
	if r.cmd == cmd {
		r.cmd = nil
		r.stdin = nil
	}
	td := r.turnDone
	r.turnDone = nil
	r.mu.Unlock()

	if td != nil {
		if tail != "" {
			td <- fmt.Errorf("Claude Code 已退出：%s", truncateRunes(tail, 300))
		} else {
			td <- errors.New("Claude Code 进程已退出")
		}
	}
}

func (r *claudeSessionRunner) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var base struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		SessionID string          `json:"session_id"`
		RequestID string          `json:"request_id"`
		IsError   bool            `json:"is_error"`
		Result    string          `json:"result"`
		Event     json.RawMessage `json:"event"`
		Message   json.RawMessage `json:"message"`
		Request   json.RawMessage `json:"request"`
		SlashCmds []string        `json:"slash_commands"`
	}
	if json.Unmarshal(line, &base) != nil {
		return
	}
	if base.SessionID != "" {
		r.mu.Lock()
		prev := r.nativeID
		r.nativeID = base.SessionID
		r.mu.Unlock()
		if prev != base.SessionID && r.onNativeID != nil {
			r.onNativeID(base.SessionID)
		}
	}

	switch base.Type {
	case "system":
		if len(base.SlashCmds) > 0 && r.onCommands != nil {
			cmds := make([]CommandOption, 0, len(base.SlashCmds))
			for _, name := range base.SlashCmds {
				name = strings.TrimPrefix(strings.TrimSpace(name), "/")
				if name != "" {
					cmds = append(cmds, CommandOption{Name: name})
				}
			}
			r.onCommands(cmds)
		}

	case "stream_event":
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
		}
		if json.Unmarshal(base.Event, &ev) == nil && ev.Type == "content_block_delta" {
			switch ev.Delta.Type {
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					r.mu.Lock()
					r.streamed = true
					r.mu.Unlock()
					r.onDelta("thinking", ev.Delta.Thinking)
				}
			case "text_delta":
				if ev.Delta.Text != "" {
					r.mu.Lock()
					r.streamed = true
					r.mu.Unlock()
					r.onDelta("text", ev.Delta.Text)
				}
			}
		}

	case "assistant":
		var msg struct {
			Content []struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				Thinking string          `json:"thinking"`
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
			} `json:"content"`
			Usage struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(base.Message, &msg) != nil {
			return
		}
		r.mu.Lock()
		alreadyStreamed := r.streamed
		r.mu.Unlock()

		for _, c := range msg.Content {
			switch c.Type {
			case "thinking":
				if !alreadyStreamed && c.Thinking != "" {
					r.onDelta("thinking", c.Thinking)
				}
			case "text":
				if !alreadyStreamed && c.Text != "" {
					r.onDelta("text", c.Text)
				}
			case "tool_use":
				detail := extractToolDetail(c.Input, nil, nil)
				title := c.Name
				if detail != "" {
					title = truncateRunes(firstNonEmptyLine(detail), 72)
				}
				r.onTool(ToolCall{
					ID:     c.ID,
					Name:   c.Name,
					Title:  title,
					Status: "in_progress",
					Detail: detail,
				})
			}
		}
		if msg.Usage.InputTokens+msg.Usage.OutputTokens > 0 && r.onUsage != nil {
			r.onUsage(msg.Usage.InputTokens+msg.Usage.OutputTokens, 200000)
		}

	case "user":
		var msg struct {
			Content []struct {
				Type      string          `json:"type"`
				ToolUseID string          `json:"tool_use_id"`
				IsError   bool            `json:"is_error"`
				Content   json.RawMessage `json:"content"`
			} `json:"content"`
		}
		if json.Unmarshal(base.Message, &msg) != nil {
			return
		}
		for _, c := range msg.Content {
			if c.Type == "tool_result" && c.ToolUseID != "" {
				st := "completed"
				if c.IsError {
					st = "failed"
				}
				r.onTool(ToolCall{
					ID:     c.ToolUseID,
					Status: st,
					Output: extractToolOutput(c.Content, nil),
				})
			}
		}

	case "control_request":
		go r.handleControlRequest(base.RequestID, base.Request)

	case "result":
		r.mu.Lock()
		td := r.turnDone
		r.turnDone = nil
		alreadyStreamed := r.streamed
		r.mu.Unlock()
		if !alreadyStreamed && !base.IsError && strings.TrimSpace(base.Result) != "" {
			r.onDelta("text", base.Result)
		}
		if td != nil {
			if base.IsError {
				errTxt := strings.TrimSpace(base.Result)
				if errTxt == "" {
					errTxt = "Claude Code 执行出错"
				}
				td <- errors.New(errTxt)
			} else {
				td <- nil
			}
		}
	}
}

func (r *claudeSessionRunner) handleControlRequest(reqID string, raw json.RawMessage) {
	var cr struct {
		Subtype  string          `json:"subtype"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &cr) != nil || cr.Subtype != "can_use_tool" {
		return
	}
	chosen := "allow_once"
	if r.onPerm != nil {
		chosen = r.onPerm(context.Background(), permWireRequest{
			ToolCall: permWireToolCall{
				Title: cr.ToolName,
				Name:  cr.ToolName,
				Args:  cr.Input,
			},
			Options: []PermOption{
				{OptionID: "allow_once", Name: "允许", Kind: "allow_once"},
				{OptionID: "reject_once", Name: "拒绝", Kind: "reject_once"},
			},
		})
	}
	var respPayload map[string]any
	if chosen == "" || strings.Contains(chosen, "reject") {
		respPayload = map[string]any{
			"behavior": "deny",
			"message":  "Rejected by user",
		}
	} else {
		var inputVal any = map[string]any{}
		if len(cr.Input) > 0 {
			_ = json.Unmarshal(cr.Input, &inputVal)
		}
		respPayload = map[string]any{
			"behavior":     "allow",
			"updatedInput": inputVal,
		}
	}
	reply, _ := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
			"response":   respPayload,
		},
	})
	_ = r.writeStdin(append(reply, '\n'))
}

// ============================================================================
// 2. OpenAI Codex CLI 驱动（codex app-server JSON-RPC 2.0 over stdio）
// ============================================================================

type codexClient struct {
	exe       string
	wrapCmd   func(*exec.Cmd) error
	onDelta   func(threadID, kind, text string)
	onTool    func(threadID string, tc ToolCall)
	onPerm    func(ctx context.Context, threadID string, req permWireRequest) string
	onTurnEnd func(threadID string, err error)
	onDead    func(err error)

	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]chan rpcResult
	nextID    atomic.Int64
	done      chan struct{}
	closeOnce sync.Once
}

func startCodexClient(
	exe string,
	wrapCmd func(*exec.Cmd) error,
	onDelta func(threadID, kind, text string),
	onTool func(threadID string, tc ToolCall),
	onPerm func(ctx context.Context, threadID string, req permWireRequest) string,
	onTurnEnd func(threadID string, err error),
	onDead func(err error),
) (*codexClient, error) {
	cmd := exec.Command(exe, "app-server")
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
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动 codex app-server：%w", err)
	}
	c := &codexClient{
		exe:       exe,
		wrapCmd:   wrapCmd,
		onDelta:   onDelta,
		onTool:    onTool,
		onPerm:    onPerm,
		onTurnEnd: onTurnEnd,
		onDead:    onDead,
		cmd:       cmd,
		stdin:     stdin,
		pending:   map[int64]chan rpcResult{},
		done:      make(chan struct{}),
	}
	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(&stderrBuf, io.LimitReader(stderr, 8192))
	}()
	go c.readLoop(stdout, stderrDone, &stderrBuf)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "frog", "title": "frog", "version": "1.0"},
	}, nil); err != nil {
		c.Close()
		return nil, fmt.Errorf("初始化 codex app-server 失败：%w", err)
	}
	_ = c.writeRaw(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	return c, nil
}

func (c *codexClient) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *codexClient) Close() {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		select {
		case <-c.done:
			return
		case <-time.After(1 * time.Second):
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
	})
}

func (c *codexClient) writeRaw(msg any) error {
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

func (c *codexClient) call(ctx context.Context, method string, params any, out any) error {
	select {
	case <-c.done:
		return errors.New("codex app-server 已断开")
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
		return errors.New("codex app-server 进程已退出")
	}
}

func (c *codexClient) readLoop(r io.Reader, stderrDone <-chan struct{}, stderrBuf *bytes.Buffer) {
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
	<-stderrDone
	_ = c.cmd.Wait()
	close(c.done)
	if readErr == nil {
		if tail := strings.TrimSpace(stderrBuf.String()); tail != "" {
			readErr = fmt.Errorf("codex app-server 已退出：%s", truncateRunes(tail, 300))
		} else {
			readErr = errors.New("codex app-server 已退出")
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

func (c *codexClient) dispatch(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var env rpcEnvelope
	if json.Unmarshal(line, &env) != nil {
		return
	}
	if env.Method != "" && len(env.ID) > 0 && string(env.ID) != "null" {
		go c.handleServerRequest(env)
		return
	}
	if env.Method != "" {
		c.handleNotification(env.Method, env.Params)
		return
	}
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

func (c *codexClient) handleNotification(method string, raw json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
		Delta    string `json:"delta"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Item struct {
			ID               string `json:"id"`
			Type             string `json:"type"`
			Command          string `json:"command"`
			AggregatedOutput string `json:"aggregatedOutput"`
			Status           string `json:"status"`
			Tool             string `json:"tool"`
			Query            string `json:"query"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ThreadID == "" {
		return
	}

	switch method {
	case "item/agentMessage/delta":
		if p.Delta != "" && c.onDelta != nil {
			c.onDelta(p.ThreadID, "text", p.Delta)
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		if p.Delta != "" && c.onDelta != nil {
			c.onDelta(p.ThreadID, "thinking", p.Delta)
		}
	case "item/started", "item/completed":
		if c.onTool == nil {
			return
		}
		switch p.Item.Type {
		case "commandExecution", "fileChange", "mcpToolCall", "webSearch":
			st := "in_progress"
			if method == "item/completed" {
				if p.Item.Status == "failed" || p.Item.Status == "declined" {
					st = "failed"
				} else {
					st = "completed"
				}
			}
			title := p.Item.Type
			detail := ""
			if p.Item.Command != "" {
				title = truncateRunes(firstNonEmptyLine(p.Item.Command), 72)
				detail = p.Item.Command
			} else if p.Item.Tool != "" {
				title = p.Item.Tool
			} else if p.Item.Query != "" {
				title = "Search: " + truncateRunes(p.Item.Query, 60)
				detail = p.Item.Query
			}
			c.onTool(p.ThreadID, ToolCall{
				ID:     p.Item.ID,
				Name:   p.Item.Type,
				Title:  title,
				Status: st,
				Detail: detail,
				Output: truncateStr(p.Item.AggregatedOutput, 4000),
			})
		}
	case "turn/completed":
		if c.onTurnEnd != nil {
			var terr error
			if p.Turn.Error != nil && p.Turn.Error.Message != "" {
				terr = errors.New(p.Turn.Error.Message)
			}
			c.onTurnEnd(p.ThreadID, terr)
		}
	}
}

func (c *codexClient) handleServerRequest(env rpcEnvelope) {
	if !strings.Contains(strings.ToLower(env.Method), "approval") {
		_ = c.writeRaw(map[string]any{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"error":   map[string]any{"code": -32601, "message": "unsupported"},
		})
		return
	}
	var p struct {
		ThreadID string          `json:"threadId"`
		Command  string          `json:"command"`
		Reason   string          `json:"reason"`
		Changes  json.RawMessage `json:"changes"`
	}
	_ = json.Unmarshal(env.Params, &p)
	title := "执行操作"
	detail := p.Command
	if detail == "" && p.Reason != "" {
		detail = p.Reason
	}
	if strings.Contains(env.Method, "command") || p.Command != "" {
		title = "运行命令"
	} else if strings.Contains(env.Method, "file") || strings.Contains(env.Method, "Patch") {
		title = "修改文件"
	}

	chosen := "accept"
	if c.onPerm != nil {
		chosen = c.onPerm(context.Background(), p.ThreadID, permWireRequest{
			ToolCall: permWireToolCall{
				Title: title,
				Name:  env.Method,
			},
			Options: []PermOption{
				{OptionID: "accept", Name: "允许", Kind: "allow_once"},
				{OptionID: "decline", Name: "拒绝", Kind: "reject_once"},
			},
		})
	}
	decision := "accept"
	if chosen == "" || chosen == "decline" || strings.Contains(chosen, "reject") {
		decision = "decline"
	}
	_ = c.writeRaw(map[string]any{
		"jsonrpc": "2.0",
		"id":      env.ID,
		"result":  map[string]string{"decision": decision},
	})
}

func codexModeParams(mode string) (approval, sandbox string) {
	switch mode {
	case "full-access":
		return "never", "danger-full-access"
	case "on-request", "default":
		return "on-request", "workspace-write"
	default:
		return "never", "workspace-write"
	}
}

// ============================================================================
// 3. Pi Coding Agent 驱动（pi --mode rpc 原生 JSONL 协议）
// ============================================================================

type piSessionRunner struct {
	exe         string
	cwd         string
	model       string
	sessionFile string
	wrapCmd     func(*exec.Cmd) error
	onDelta     func(kind, text string)
	onTool      func(tc ToolCall)

	mu       sync.Mutex
	writeMu  sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	turnDone chan error
	closed   bool
}

func (p *piSessionRunner) ensureStarted() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("会话已关闭")
	}
	if p.cmd != nil && p.stdin != nil {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(p.sessionFile), 0o700)
	args := []string{"--mode", "rpc", "--session", p.sessionFile}
	if p.model != "" && p.model != "default" {
		args = append(args, "--model", p.model)
	}
	cmd := exec.Command(p.exe, args...)
	cmd.Dir = p.cwd
	if p.wrapCmd != nil {
		if err := p.wrapCmd(cmd); err != nil {
			return err
		}
	}
	cmd.Env = buildChildEnv(p.exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法启动 pi --mode rpc：%w", err)
	}
	p.cmd = cmd
	p.stdin = stdin
	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(&stderrBuf, io.LimitReader(stderr, 8192))
	}()
	go p.readLoop(cmd, stdout, stderrDone, &stderrBuf)
	return nil
}

func (p *piSessionRunner) writeStdin(line []byte) error {
	p.mu.Lock()
	stdin := p.stdin
	p.mu.Unlock()
	if stdin == nil {
		return errors.New("pi 输入流不可用")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := stdin.Write(line)
	return err
}

func (p *piSessionRunner) RunTurn(ctx context.Context, prompt string) (string, error) {
	if err := p.ensureStarted(); err != nil {
		return "", err
	}
	doneCh := make(chan error, 1)
	p.mu.Lock()
	p.turnDone = doneCh
	p.mu.Unlock()

	raw, _ := json.Marshal(map[string]any{
		"type":    "prompt",
		"message": prompt,
	})
	if err := p.writeStdin(append(raw, '\n')); err != nil {
		p.Kill()
		return "", err
	}
	select {
	case err := <-doneCh:
		if err != nil {
			return "", err
		}
		return "end_turn", nil
	case <-ctx.Done():
		p.Abort()
		return "cancelled", ctx.Err()
	}
}

func (p *piSessionRunner) Abort() {
	raw, _ := json.Marshal(map[string]string{"type": "abort"})
	_ = p.writeStdin(append(raw, '\n'))
}

func (p *piSessionRunner) Kill() {
	p.mu.Lock()
	stdin := p.stdin
	cmd := p.cmd
	p.stdin = nil
	p.cmd = nil
	p.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (p *piSessionRunner) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.Kill()
}

func (p *piSessionRunner) readLoop(cmd *exec.Cmd, stdout io.Reader, stderrDone <-chan struct{}, stderrBuf *bytes.Buffer) {
	br := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := readLineLimited(br, maxRPCLine)
		if len(line) > 0 {
			p.handleLine(line)
		}
		if err != nil {
			break
		}
	}
	<-stderrDone
	_ = cmd.Wait()
	tail := strings.TrimSpace(stderrBuf.String())
	p.mu.Lock()
	if p.cmd == cmd {
		p.cmd = nil
		p.stdin = nil
	}
	td := p.turnDone
	p.turnDone = nil
	p.mu.Unlock()
	if td != nil {
		if tail != "" {
			td <- fmt.Errorf("pi 已退出：%s", truncateRunes(tail, 300))
		} else {
			td <- errors.New("pi 进程已退出")
		}
	}
}

func (p *piSessionRunner) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var ev struct {
		Type       string          `json:"type"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       json.RawMessage `json:"args"`
		Result     json.RawMessage `json:"result"`
		IsError    bool            `json:"isError"`
		Success    *bool           `json:"success"`
		Error      string          `json:"error"`
		AsstEvent  *struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_update":
		if ev.AsstEvent != nil && ev.AsstEvent.Delta != "" {
			switch ev.AsstEvent.Type {
			case "text_delta":
				p.onDelta("text", ev.AsstEvent.Delta)
			case "thinking_delta":
				p.onDelta("thinking", ev.AsstEvent.Delta)
			}
		}
	case "tool_execution_start":
		detail := extractToolDetail(ev.Args, nil, nil)
		title := ev.ToolName
		if detail != "" {
			title = truncateRunes(firstNonEmptyLine(detail), 72)
		}
		p.onTool(ToolCall{
			ID:     ev.ToolCallID,
			Name:   ev.ToolName,
			Title:  title,
			Status: "in_progress",
			Detail: detail,
		})
	case "tool_execution_end":
		st := "completed"
		if ev.IsError {
			st = "failed"
		}
		p.onTool(ToolCall{
			ID:     ev.ToolCallID,
			Name:   ev.ToolName,
			Status: st,
			Output: extractToolOutput(ev.Result, nil),
		})
	case "response":
		if ev.Success != nil && !*ev.Success && ev.Error != "" {
			p.mu.Lock()
			td := p.turnDone
			p.turnDone = nil
			p.mu.Unlock()
			if td != nil {
				td <- errors.New(ev.Error)
			}
		}
	case "agent_end":
		p.mu.Lock()
		td := p.turnDone
		p.turnDone = nil
		p.mu.Unlock()
		if td != nil {
			td <- nil
		}
	}
}
