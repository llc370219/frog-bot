package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"frogbot/internal/vault"
)

var handlePlaceholderRe = regexp.MustCompile(`\{\{secret:(?://)?([A-Za-z][A-Za-z0-9_.-]{0,63})\}\}`)

// ExecRequest is sent from the low-privilege `frog-exec` CLI to the Host Daemon over Unix socket.
type ExecRequest struct {
	BotID         string            `json:"bot_id"`
	Cwd           string            `json:"cwd,omitempty"`
	Command       string            `json:"command"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"` // may contain {{secret://name}} placeholders
	TimeoutSec    int               `json:"timeout_sec,omitempty"`
	Tainted       bool              `json:"tainted,omitempty"`
	TaintSources  []TaintSource     `json:"taint_sources,omitempty"`
	TriggerSource string            `json:"trigger_source,omitempty"`
}

// ExecResponse is returned to `frog-exec` with all outputs multi-encoding scrubbed.
type ExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Denied   bool   `json:"denied,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// WorkspaceResolver maps a botID to its allowed workspace root directory.
type WorkspaceResolver func(botID string) (string, bool)

// ExecBridge hosts the Unix socket server backing `frog-exec`, allowing low-privilege
// scripts and Coding CLIs in Auto mode to self-test code containing {{secret://...}}
// without ever reading vault.enc or seeing secret plaintext.
type ExecBridge struct {
	SocketPath string
	Vault      *vault.Vault
	Gateway    *SecurityGateway
	ResolveWS  WorkspaceResolver
	WrapCmd    func(cmd *exec.Cmd, workspace string) error // optional UID drop / Landlock / bwrap

	mu       sync.Mutex
	listener net.Listener
	closed   bool
	wg       sync.WaitGroup
}

func NewExecBridge(sockPath string, vlt *vault.Vault, gw *SecurityGateway, resolveWS WorkspaceResolver) *ExecBridge {
	return &ExecBridge{
		SocketPath: sockPath,
		Vault:      vlt,
		Gateway:    gw,
		ResolveWS:  resolveWS,
	}
}

func (b *ExecBridge) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(b.SocketPath), 0o750); err != nil {
		return err
	}
	_ = os.Remove(b.SocketPath)
	ln, err := net.Listen("unix", b.SocketPath)
	if err != nil {
		return fmt.Errorf("listen frog-exec socket: %w", err)
	}
	_ = os.Chmod(b.SocketPath, 0o660)

	b.mu.Lock()
	b.listener = ln
	b.mu.Unlock()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		go func() {
			<-ctx.Done()
			_ = ln.Close()
		}()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			b.wg.Add(1)
			go func(c net.Conn) {
				defer b.wg.Done()
				defer c.Close()
				b.handleConn(ctx, c)
			}(conn)
		}
	}()
	return nil
}

func (b *ExecBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	ln := b.listener
	b.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	b.wg.Wait()
	_ = os.Remove(b.SocketPath)
	return nil
}

// ServeConn handles a single framed ExecRequest/ExecResponse connection (used by Unix socket listener & in-process tests).
func (b *ExecBridge) ServeConn(ctx context.Context, conn net.Conn) {
	b.handleConn(ctx, conn)
}

func (b *ExecBridge) handleConn(ctx context.Context, conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(12 * time.Minute))
	var req ExecRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(ExecResponse{ExitCode: 2, Denied: true, Reason: "无效请求: " + err.Error()})
		return
	}
	resp := b.Execute(ctx, req)
	_ = json.NewEncoder(conn).Encode(resp)
}

// Execute adjudicates, injects secrets in isolated host memory, runs the command, and scrubs outputs.
func (b *ExecBridge) Execute(ctx context.Context, req ExecRequest) ExecResponse {
	if strings.TrimSpace(req.Command) == "" {
		return ExecResponse{ExitCode: 2, Denied: true, Reason: "command 不能为空"}
	}
	botID := req.BotID
	if botID == "" {
		botID = "main"
	}
	wsRoot := ""
	if b.ResolveWS != nil {
		var ok bool
		wsRoot, ok = b.ResolveWS(botID)
		if !ok {
			return ExecResponse{ExitCode: 126, Denied: true, Reason: fmt.Sprintf("未知 Bot 工作区: %s", botID)}
		}
	}
	cwd := req.Cwd
	if wsRoot != "" {
		wsAbs, _ := filepath.Abs(wsRoot)
		if cwd == "" {
			cwd = wsAbs
		} else {
			cwdAbs, _ := filepath.Abs(cwd)
			rel, err := filepath.Rel(wsAbs, cwdAbs)
			if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
				return ExecResponse{ExitCode: 126, Denied: true, Reason: "cwd 超出 Bot 工作区边界"}
			}
			cwd = cwdAbs
		}
	}

	// 1. Collect all {{secret://...}} handles referenced in command, args, and env
	handles := extractAllHandles(req)
	fullCmdText := req.Command + " " + strings.Join(req.Args, " ")
	domains := ExtractDomains(fullCmdText)
	for _, v := range req.Env {
		domains = append(domains, ExtractDomains(v)...)
	}

	// 2. Enforce independent SecurityGateway adjudication (zero plaintext seen by gateway)
	if b.Gateway != nil {
		trigger := req.TriggerSource
		if trigger == "" {
			trigger = "frog-exec"
		}
		dec := b.Gateway.Enforce(ctx, ReviewInput{
			BotID:         botID,
			Action:        "frog_exec",
			TriggerSource: trigger,
			TargetDomains: domains,
			Summary:       fmt.Sprintf("frog-exec 执行: %s", truncateStr(fullCmdText, 240)),
			Command:       fullCmdText,
			SecretHandles: handles,
			Taint:         TaintSnapshot{Tainted: req.Tainted, Sources: req.TaintSources},
		})
		if dec.Verdict != VerdictApprove {
			return ExecResponse{
				ExitCode: 126,
				Denied:   true,
				Reason:   dec.Reason,
				Stderr:   fmt.Sprintf("frog-exec DENIED (%s): %s\n", dec.By, dec.Reason),
			}
		}
	}

	// 3. Resolve plaintext values strictly in isolated host RAM
	vals := map[string]string{}
	if len(handles) > 0 {
		if b.Vault == nil {
			return ExecResponse{ExitCode: 126, Denied: true, Reason: "宿主未配置 Blind Vault"}
		}
		var err error
		vals, err = b.Vault.Values(handles)
		if err != nil {
			return ExecResponse{ExitCode: 126, Denied: true, Reason: b.Vault.Scrub(err.Error())}
		}
	}

	// 4. Substitute handles in isolated memory
	sub := func(s string) string {
		return handlePlaceholderRe.ReplaceAllStringFunc(s, func(m string) string {
			subm := handlePlaceholderRe.FindStringSubmatch(m)
			if len(subm) < 2 {
				return m
			}
			if val, ok := vals[subm[1]]; ok {
				return val
			}
			return m
		})
	}
	cmdName := sub(req.Command)
	args := make([]string, len(req.Args))
	for i, a := range req.Args {
		args[i] = sub(a)
	}

	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 2 * time.Minute
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, cmdName, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = sanitizedExecEnv(req.Env, vals, sub)
	if b.WrapCmd != nil {
		if err := b.WrapCmd(cmd, wsRoot); err != nil {
			return ExecResponse{ExitCode: 126, Denied: true, Reason: "隔离包装失败: " + err.Error()}
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdoutBuf, max: 256 << 10}
	cmd.Stderr = &limitedWriter{buf: &stderrBuf, max: 256 << 10}

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
			stderrBuf.WriteString("\n" + err.Error())
		}
	}

	// 5. Multi-encoding & high-entropy scrubbing before returning to worker/CLI
	stdoutStr := stdoutBuf.String()
	stderrStr := stderrBuf.String()
	if b.Vault != nil {
		stdoutStr = b.Vault.Scrub(stdoutStr)
		stderrStr = b.Vault.Scrub(stderrStr)
	}
	return ExecResponse{
		ExitCode: exitCode,
		Stdout:   stdoutStr,
		Stderr:   stderrStr,
	}
}

// RunExecConn runs a frog-exec RPC over an already established net.Conn.
func RunExecConn(conn net.Conn, req ExecRequest) (ExecResponse, error) {
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return ExecResponse{}, err
	}
	var resp ExecResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return ExecResponse{}, err
	}
	return resp, nil
}

// RunExecClient dials the Host Daemon Unix socket and executes a frog-exec request.
func RunExecClient(sockPath string, req ExecRequest) (ExecResponse, error) {
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return ExecResponse{}, fmt.Errorf("连接 frog-exec 宿主桥 (%s) 失败: %w", sockPath, err)
	}
	defer conn.Close()
	return RunExecConn(conn, req)
}

func extractAllHandles(req ExecRequest) []string {
	seen := map[string]bool{}
	scan := func(s string) {
		for _, m := range handlePlaceholderRe.FindAllStringSubmatch(s, -1) {
			seen[m[1]] = true
		}
	}
	scan(req.Command)
	for _, a := range req.Args {
		scan(a)
	}
	for _, v := range req.Env {
		scan(v)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sanitizedExecEnv(extra map[string]string, vals map[string]string, sub func(string) string) []string {
	drop := map[string]bool{
		"FROGBOT_MASTER_KEY": true,
		"FROGBOT_API_KEY":    true,
		"FROGBOT_PASSWORD":   true,
	}
	out := make([]string, 0, 16+len(extra))
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || drop[k] {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		if drop[strings.ToUpper(k)] {
			continue
		}
		out = append(out, k+"="+sub(v))
	}
	// Also expose referenced secrets under FROG_SECRET_<HANDLE> for scripts that read env vars directly.
	for name, val := range vals {
		envName := "FROG_SECRET_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
		out = append(out, envName+"="+val)
	}
	return out
}

type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	rem := w.max - w.buf.Len()
	if rem > 0 {
		if len(p) > rem {
			w.buf.Write(p[:rem])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
