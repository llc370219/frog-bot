package clipool

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"frogbot/internal/agy"
	"frogbot/internal/host"
	"frogbot/internal/vault"
)

const MaxCallbackSummaryChars = 800

// CLITaskRequest requests an async heavy coding/repair job on the LocalCLIPool.
type CLITaskRequest struct {
	BotID          string   `json:"bot_id"`
	Mode           string   `json:"mode"` // "plan" | "auto"
	CLIPreference  string   `json:"cli_preference,omitempty"`
	FallbackChain  []string `json:"fallback_chain,omitempty"`
	TaskPrompt     string   `json:"task_prompt"`
	Workspace      string   `json:"workspace"`
	LogsDir        string   `json:"logs_dir"`
	TimeoutSec     int      `json:"timeout_sec,omitempty"`
	Priority       Priority `json:"priority,omitempty"`
	ExecSocketPath string   `json:"exec_socket_path,omitempty"`
	Tainted        bool     `json:"tainted,omitempty"`
}

// CLITaskState tracks the lifecycle of one dispatched Coding CLI task.
type CLITaskState struct {
	ID           string    `json:"id"`
	BotID        string    `json:"bot_id"`
	CLI          string    `json:"cli"`
	Mode         string    `json:"mode"`
	Status       string    `json:"status"` // queued | running | completed | failed | cancelled
	LogPath      string    `json:"log_path"`
	PromptBrief  string    `json:"prompt_brief"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
	ExitCode     int       `json:"exit_code"`
	Summary      string    `json:"summary,omitempty"`
	ChangedFiles []string  `json:"changed_files,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// CLITaskCallback is the condensed <=800-char payload delivered back to the Bot Actor Inbox.
type CLITaskCallback struct {
	TaskID       string        `json:"task_id"`
	BotID        string        `json:"bot_id"`
	CLI          string        `json:"cli"`
	Mode         string        `json:"mode"`
	Status       string        `json:"status"`
	Summary      string        `json:"summary"`
	ChangedFiles []string      `json:"changed_files,omitempty"`
	LogPath      string        `json:"log_path"`
	Duration     time.Duration `json:"duration"`
	Error        string        `json:"error,omitempty"`
}

// CommandFactory builds the exec.Cmd for a selected CLI + mode + prompt.
// Overridable in tests to plug in deterministic mock CLIs.
type CommandFactory func(ctx context.Context, cli, mode, prompt, workspace string) (*exec.Cmd, error)

type activeTask struct {
	state  CLITaskState
	cancel context.CancelFunc
	cmdMu  sync.Mutex
	cmd    *exec.Cmd
}

// LocalCLIPool manages Host-global official Coding CLI execution with OS isolation,
// workspace_write lease locks, silent log streaming, and <=800-char Actor callbacks.
type LocalCLIPool struct {
	Quota         *QuotaManager
	Leases        *host.LeaseManager
	Vault         *vault.Vault
	Isolation     IsolationOptions
	BuildCommand  CommandFactory
	ExtraEnv      func() map[string]string
	OnLogChunk    func(taskID, botID, chunk string)
	OnComplete    func(cb CLITaskCallback)

	mu    sync.RWMutex
	tasks map[string]*activeTask
	wg    sync.WaitGroup
}

func NewLocalCLIPool(quota *QuotaManager, leases *host.LeaseManager, vlt *vault.Vault, iso IsolationOptions) *LocalCLIPool {
	if quota == nil {
		quota = NewQuotaManager(nil, 2, 15.0)
	}
	if leases == nil {
		leases = host.NewLeaseManager()
	}
	p := &LocalCLIPool{
		Quota:     quota,
		Leases:    leases,
		Vault:     vlt,
		Isolation: iso,
		tasks:     make(map[string]*activeTask),
	}
	p.BuildCommand = defaultCLICommand
	if quota.AvailabilityCheck == nil {
		quota.AvailabilityCheck = func(cli string) bool {
			_, err := agy.ResolveAgentBinary(cli)
			return err == nil
		}
	}
	return p
}

// Dispatch validates, creates, and asynchronously launches a black-box CLI task.
// Returns the task ID immediately (non-blocking for Bot chat turns).
func (p *LocalCLIPool) Dispatch(parentCtx context.Context, req CLITaskRequest) (CLITaskState, error) {
	req.TaskPrompt = strings.TrimSpace(req.TaskPrompt)
	if req.TaskPrompt == "" {
		return CLITaskState{}, errors.New("task_prompt 不能为空")
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "auto"
	}
	if mode != "plan" && mode != "auto" {
		return CLITaskState{}, fmt.Errorf("mode 必须是 plan 或 auto，收到: %s", mode)
	}
	req.Mode = mode
	if req.BotID == "" {
		req.BotID = "main"
	}
	if req.Priority == 0 {
		req.Priority = PriorityUser
	}
	if req.TimeoutSec <= 0 {
		req.TimeoutSec = 600
	}
	if req.LogsDir == "" {
		req.LogsDir = filepath.Join(req.Workspace, ".logs")
	}
	if err := os.MkdirAll(req.Workspace, 0o755); err != nil {
		return CLITaskState{}, err
	}
	if err := os.MkdirAll(req.LogsDir, 0o700); err != nil {
		return CLITaskState{}, err
	}

	selectedCLI, err := p.Quota.SelectCLI(req.CLIPreference, req.FallbackChain)
	if err != nil {
		return CLITaskState{}, err
	}

	var rb [6]byte
	_, _ = rand.Read(rb[:])
	taskID := "cli_" + hex.EncodeToString(rb[:])
	logPath := filepath.Join(req.LogsDir, taskID+".log")

	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(req.TimeoutSec)*time.Second)
	st := CLITaskState{
		ID:          taskID,
		BotID:       req.BotID,
		CLI:         selectedCLI,
		Mode:        req.Mode,
		Status:      "queued",
		LogPath:     logPath,
		PromptBrief: truncateText(req.TaskPrompt, 120),
		StartedAt:   time.Now(),
	}
	at := &activeTask{state: st, cancel: cancel}

	p.mu.Lock()
	p.tasks[taskID] = at
	p.mu.Unlock()

	p.wg.Add(1)
	go p.runTask(ctx, at, req)
	return st, nil
}

// Cancel forcefully terminates a queued or running CLI task and its entire process tree.
func (p *LocalCLIPool) Cancel(taskID string) error {
	p.mu.RLock()
	at, ok := p.tasks[taskID]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("未找到 CLI 任务: %s", taskID)
	}
	at.cancel()
	at.cmdMu.Lock()
	cmd := at.cmd
	at.cmdMu.Unlock()
	killProcessTree(cmd)
	return nil
}

// Close cancels all running CLI tasks and waits for clean teardown.
func (p *LocalCLIPool) Close() {
	p.mu.RLock()
	for _, at := range p.tasks {
		at.cancel()
		at.cmdMu.Lock()
		killProcessTree(at.cmd)
		at.cmdMu.Unlock()
	}
	p.mu.RUnlock()
	p.wg.Wait()
}

// Get returns a snapshot of a task's state.
func (p *LocalCLIPool) Get(taskID string) (CLITaskState, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	at, ok := p.tasks[taskID]
	if !ok {
		return CLITaskState{}, false
	}
	return at.state, true
}

// List returns recent CLI tasks (optionally filtered by botID).
func (p *LocalCLIPool) List(botID string) []CLITaskState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []CLITaskState
	for _, at := range p.tasks {
		if botID != "" && at.state.BotID != botID {
			continue
		}
		out = append(out, at.state)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// ReadLog returns log content starting at byte offset for client Log Drawer pagination.
func (p *LocalCLIPool) ReadLog(taskID string, offset int64) (string, int64, error) {
	st, ok := p.Get(taskID)
	if !ok {
		return "", 0, fmt.Errorf("未找到 CLI 任务: %s", taskID)
	}
	f, err := os.Open(st.LogPath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", offset, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return "", offset, err
	}
	return string(data), offset + int64(len(data)), nil
}

func (p *LocalCLIPool) runTask(ctx context.Context, at *activeTask, req CLITaskRequest) {
	defer p.wg.Done()
	defer at.cancel()

	start := time.Now()
	finish := func(status string, exitCode int, summary string, changed []string, runErr error) {
		summary = strings.TrimSpace(summary)
		if p.Vault != nil {
			summary = p.Vault.Scrub(summary)
		}
		summary = tailRunes(summary, MaxCallbackSummaryChars)
		errMsg := ""
		if runErr != nil {
			errMsg = runErr.Error()
			if p.Vault != nil {
				errMsg = p.Vault.Scrub(errMsg)
			}
		}
		p.mu.Lock()
		at.state.Status = status
		at.state.ExitCode = exitCode
		at.state.Summary = summary
		at.state.ChangedFiles = changed
		at.state.Error = errMsg
		at.state.FinishedAt = time.Now()
		cb := CLITaskCallback{
			TaskID:       at.state.ID,
			BotID:        at.state.BotID,
			CLI:          at.state.CLI,
			Mode:         at.state.Mode,
			Status:       status,
			Summary:      summary,
			ChangedFiles: changed,
			LogPath:      at.state.LogPath,
			Duration:     time.Since(start),
			Error:        errMsg,
		}
		p.mu.Unlock()
		if p.OnComplete != nil {
			p.OnComplete(cb)
		}
	}

	// 1. Acquire workspace_write:<bot> lease lock for auto mode so concurrent edits / cron retries never collide
	var lease *host.Lease
	if req.Mode == "auto" {
		var err error
		leaseTTL := time.Duration(req.TimeoutSec+60) * time.Second
		lease, err = p.Leases.Acquire(ctx, host.WorkspaceWriteResource(req.BotID), at.state.ID, leaseTTL)
		if err != nil {
			finish("cancelled", -1, "", nil, fmt.Errorf("等待工作区写入锁失败: %w", err))
			return
		}
		defer lease.Release()
	}

	// 2. Acquire priority slot on selected CLI (with one automatic failover retry on mid-run rate limit)
	tried := map[string]bool{}
	var lastErr error
	var lastSummary string
	var changed []string

	for attempt := 0; attempt < 2; attempt++ {
		cli := at.state.CLI
		if attempt > 0 {
			nextCLI, err := p.Quota.SelectCLI("", req.FallbackChain)
			if err != nil || tried[nextCLI] {
				break
			}
			cli = nextCLI
			p.mu.Lock()
			at.state.CLI = cli
			p.mu.Unlock()
		}
		tried[cli] = true

		releaseSlot, err := p.Quota.AcquireSlot(ctx, cli, req.Priority)
		if err != nil {
			finish("cancelled", -1, "", nil, err)
			return
		}
		p.Quota.RecordDispatch(cli)
		p.mu.Lock()
		at.state.Status = "running"
		p.mu.Unlock()

		beforeSnap := snapshotWorkspace(req.Workspace)
		exitCode, tailText, rateLimited, runErr := p.executeOnce(ctx, at, cli, req)
		releaseSlot()
		changed = diffWorkspace(beforeSnap, snapshotWorkspace(req.Workspace))
		lastSummary = tailText
		lastErr = runErr

		if ctx.Err() != nil {
			finish("cancelled", exitCode, tailText, changed, ctx.Err())
			return
		}
		if runErr == nil && exitCode == 0 {
			finish("completed", 0, tailText, changed, nil)
			return
		}
		if rateLimited {
			// Mid-run 429 / rate limit: QuotaManager already placed cli in COOLDOWN; retry on fallback CLI
			continue
		}
		finish("failed", exitCode, tailText, changed, runErr)
		return
	}

	finish("failed", 1, lastSummary, changed, lastErr)
}

func (p *LocalCLIPool) executeOnce(ctx context.Context, at *activeTask, cli string, req CLITaskRequest) (int, string, bool, error) {
	augmentedPrompt := buildAugmentedPrompt(req)
	cmd, err := p.BuildCommand(ctx, cli, req.Mode, augmentedPrompt, req.Workspace)
	if err != nil {
		return 1, "", false, err
	}
	cmd.Dir = req.Workspace
	cmd.Env = p.buildWorkerEnv(req)

	iso := p.Isolation
	iso.WorkspaceDir = req.Workspace
	if err := applyIsolation(cmd, iso); err != nil {
		return 126, "", false, err
	}

	logFile, err := os.OpenFile(at.state.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 1, "", false, err
	}
	defer logFile.Close()
	_, _ = fmt.Fprintf(logFile, "=== Frog Bot V2 CLI Task %s | cli=%s | mode=%s | started=%s ===\n",
		at.state.ID, cli, req.Mode, time.Now().Format(time.RFC3339))

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return 1, "", false, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return 1, "", false, err
	}

	at.cmdMu.Lock()
	if ctx.Err() != nil {
		at.cmdMu.Unlock()
		return 1, "", false, ctx.Err()
	}
	if err := cmd.Start(); err != nil {
		at.cmdMu.Unlock()
		return 1, "", false, fmt.Errorf("启动 %s 失败: %w", cli, err)
	}
	at.cmd = cmd
	at.cmdMu.Unlock()

	var (
		outMu       sync.Mutex
		tailLines   []string
		rateLimited bool
	)
	consume := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 32*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if p.Quota.SniffLine(cli, line) {
				outMu.Lock()
				rateLimited = true
				outMu.Unlock()
			}
			cleanLine := line
			if p.Vault != nil {
				cleanLine = p.Vault.Scrub(cleanLine)
			}
			outMu.Lock()
			_, _ = logFile.WriteString(cleanLine + "\n")
			tailLines = append(tailLines, cleanLine)
			if len(tailLines) > 40 {
				tailLines = tailLines[len(tailLines)-40:]
			}
			outMu.Unlock()
			if p.OnLogChunk != nil {
				p.OnLogChunk(at.state.ID, req.BotID, cleanLine+"\n")
			}
		}
	}

	var pipeWG sync.WaitGroup
	pipeWG.Add(2)
	go func() { defer pipeWG.Done(); consume(stdoutPipe) }()
	go func() { defer pipeWG.Done(); consume(stderrPipe) }()
	pipeWG.Wait()

	waitErr := cmd.Wait()
	at.cmdMu.Lock()
	at.cmd = nil
	at.cmdMu.Unlock()

	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}
	outMu.Lock()
	summary := strings.Join(tailLines, "\n")
	rl := rateLimited
	outMu.Unlock()
	return exitCode, summary, rl, waitErr
}

func buildAugmentedPrompt(req CLITaskRequest) string {
	hint := "[Frog Bot 环境提示] 你的工作区被隔离在当前目录。若脚本需要测试包含密钥句柄 {{secret://name}} 的命令，请通过 `frog-exec <命令> [参数]` 调用宿主受控执行桥完成自测，切勿尝试读取宿主目录或打印密钥明文。完成后请用 ≤500 字总结变更与验证结果。\n\n"
	if req.Mode == "plan" {
		hint += "[模式: PLAN 只读规划] 仅分析代码并输出清晰修改方案，不要直接修改文件。\n\n"
	}
	return hint + req.TaskPrompt
}

func (p *LocalCLIPool) buildWorkerEnv(req CLITaskRequest) []string {
	drop := map[string]bool{
		"FROGBOT_MASTER_KEY": true,
		"FROGBOT_API_KEY":    true,
		"FROGBOT_PASSWORD":   true,
	}
	out := make([]string, 0, 32)
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || drop[k] {
			continue
		}
		out = append(out, kv)
	}
	if req.ExecSocketPath != "" {
		out = append(out, "FROG_EXEC_SOCK="+req.ExecSocketPath)
	}
	out = append(out, "FROG_BOT_ID="+req.BotID)
	if req.Tainted {
		out = append(out, "FROG_TAINTED=1")
	}
	if p.ExtraEnv != nil {
		existing := map[string]bool{}
		for _, kv := range out {
			if k, _, ok := strings.Cut(kv, "="); ok {
				existing[k] = true
			}
		}
		for k, v := range p.ExtraEnv() {
			if k != "" && v != "" && !existing[k] {
				out = append(out, k+"="+v)
			}
		}
	}
	return out
}

func defaultCLICommand(ctx context.Context, cli, mode, prompt, workspace string) (*exec.Cmd, error) {
	exe, err := agy.ResolveAgentBinary(cli)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(cli) {
	case "claude":
		args := []string{"-p", "--output-format", "text"}
		if mode == "plan" {
			args = append(args, "--permission-mode", "plan")
		} else {
			args = append(args, "--dangerously-skip-permissions")
		}
		args = append(args, prompt)
		return exec.CommandContext(ctx, exe, args...), nil
	case "codex":
		args := []string{"exec"}
		if mode == "plan" {
			args = append(args, "--sandbox", "read-only")
		} else {
			args = append(args, "--full-auto")
		}
		args = append(args, prompt)
		return exec.CommandContext(ctx, exe, args...), nil
	case "gemini":
		args := []string{"-p", prompt}
		if mode == "auto" {
			args = append(args, "--yolo")
		}
		return exec.CommandContext(ctx, exe, args...), nil
	case "grok":
		return exec.CommandContext(ctx, exe, "-p", prompt), nil
	default:
		return exec.CommandContext(ctx, exe, "-p", prompt), nil
	}
}

func snapshotWorkspace(root string) map[string]time.Time {
	snap := map[string]time.Time{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".logs" {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			if rel, err := filepath.Rel(root, path); err == nil {
				snap[rel] = info.ModTime()
			}
		}
		return nil
	})
	return snap
}

func diffWorkspace(before, after map[string]time.Time) []string {
	var changed []string
	for rel, mt := range after {
		if prev, ok := before[rel]; !ok || !prev.Equal(mt) {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	if len(changed) > 30 {
		changed = changed[:30]
	}
	return changed
}

func tailRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-maxRunes+1:])
}
