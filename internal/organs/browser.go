package organs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frogbot/internal/host"
)

// AntiExportPlaywrightGuard is the host-injected Node ESM wrapper that runs Playwright scripts
// with persistent browser_profile/ while blocking bulk cookie/storageState credential exports.
const AntiExportPlaywrightGuard = `
// Frog Bot V2 Host-Controlled Playwright Wrapper (Anti-Export Guard)
export function wrapProtectedContext(ctx) {
  const deny = (method) => () => {
    throw new Error('SECURITY_DENIED: Frog Bot Host policy forbids calling ' + method + '() to export credentials.');
  };
  return new Proxy(ctx, {
    get(target, prop, receiver) {
      if (prop === 'cookies' || prop === 'storageState') {
        return deny(String(prop));
      }
      const val = Reflect.get(target, prop, receiver);
      return typeof val === 'function' ? val.bind(target) : val;
    }
  });
}
`

// BrowserScriptRequest asks the on-demand Playwright BrowserPool to run a script
// with a bot's persistent browser_profile/ (0 MB RAM when idle).
type BrowserScriptRequest struct {
	BotID          string        `json:"bot_id"`
	ProfileDir     string        `json:"profile_dir"`
	Workspace      string        `json:"workspace"`
	ScriptPath     string        `json:"script_path"`
	Args           []string      `json:"args,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	NonBlocking    bool          `json:"non_blocking,omitempty"` // true -> TryAcquire (skip duplicate cron)
	Headless       bool          `json:"headless"`
	CDPRemotePort  int           `json:"cdp_remote_port,omitempty"`
	FixedUserAgent string        `json:"fixed_user_agent,omitempty"`
}

// BrowserScriptOutcome reports script completion or structured organ signals
// (WAITING_HUMAN captcha/login needed, or SELECTOR_CHANGED website layout changed).
type BrowserScriptOutcome struct {
	ExitCode       int    `json:"exit_code"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	SkippedBusy    bool   `json:"skipped_busy,omitempty"`
	WaitingHuman   *WaitingHumanSignal   `json:"waiting_human,omitempty"`
	SelectorBroken *SelectorBrokenSignal `json:"selector_broken,omitempty"`
}

type WaitingHumanSignal struct {
	URL            string `json:"url"`
	Reason         string `json:"reason"`
	ScreenshotPath string `json:"screenshot_path,omitempty"`
}

type SelectorBrokenSignal struct {
	URL            string `json:"url"`
	Selector       string `json:"selector"`
	Error          string `json:"error"`
	ScreenshotPath string `json:"screenshot_path,omitempty"`
	DOMSnippet     string `json:"dom_snippet,omitempty"`
}

type activeBrowserProc struct {
	botID    string
	cmd      *exec.Cmd
	lease    *host.Lease
	lastUsed time.Time
	cancel   context.CancelFunc
}

// BrowserPool manages on-demand cold-start, idle-reap Playwright/Chromium execution
// protected by browser_profile:<bot> TTL lease locks and anti-export guards.
type BrowserPool struct {
	Leases      *host.LeaseManager
	IdleTimeout time.Duration // default 3 minutes
	CommandFn   func(ctx context.Context, req BrowserScriptRequest, guardPath string) (*exec.Cmd, error)

	mu      sync.Mutex
	procs   map[string]*activeBrowserProc
	stopCh  chan struct{}
	stopped bool
	wg      sync.WaitGroup
}

func NewBrowserPool(leases *host.LeaseManager, idleTimeout time.Duration) *BrowserPool {
	if leases == nil {
		leases = host.NewLeaseManager()
	}
	if idleTimeout <= 0 {
		idleTimeout = 3 * time.Minute
	}
	p := &BrowserPool{
		Leases:      leases,
		IdleTimeout: idleTimeout,
		procs:       make(map[string]*activeBrowserProc),
		stopCh:      make(chan struct{}),
	}
	p.CommandFn = defaultBrowserCommand
	p.startIdleReaper()
	return p
}

func (p *BrowserPool) Close() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	close(p.stopCh)
	for k, ap := range p.procs {
		p.killLocked(k, ap)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *BrowserPool) startIdleReaper() {
	tick := p.IdleTimeout / 3
	if tick < 20*time.Millisecond {
		tick = 20 * time.Millisecond
	}
	if tick > 30*time.Second {
		tick = 30 * time.Second
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		tk := time.NewTicker(tick)
		defer tk.Stop()
		for {
			select {
			case <-p.stopCh:
				return
			case now := <-tk.C:
				p.reapIdle(now)
			}
		}
	}()
}

func (p *BrowserPool) reapIdle(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, ap := range p.procs {
		if now.Sub(ap.lastUsed) >= p.IdleTimeout {
			p.killLocked(id, ap)
		}
	}
}

func (p *BrowserPool) killLocked(id string, ap *activeBrowserProc) {
	if ap.cancel != nil {
		ap.cancel()
	}
	if ap.cmd != nil && ap.cmd.Process != nil {
		_ = ap.cmd.Process.Kill()
	}
	if ap.lease != nil {
		ap.lease.Release()
	}
	delete(p.procs, id)
}

// ActiveCount reports the number of currently running browser processes (0 when idle!).
func (p *BrowserPool) ActiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.procs)
}

// EnsureGuardFile writes the anti-export guard wrapper module into the bot's workspace.
func EnsureGuardFile(workspace string) (string, error) {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "", err
	}
	guardPath := filepath.Join(workspace, ".frog_pw_guard.mjs")
	if err := os.WriteFile(guardPath, []byte(AntiExportPlaywrightGuard), 0o644); err != nil {
		return "", err
	}
	return guardPath, nil
}

// RunScript cold-starts a Playwright/browser script under the browser_profile:<bot> lease lock,
// parses structured WAITING_HUMAN / SELECTOR_CHANGED signals, and immediately closes the browser
// process upon exit so idle memory drops back to 0 MB.
func (p *BrowserPool) RunScript(ctx context.Context, req BrowserScriptRequest) (BrowserScriptOutcome, error) {
	if req.BotID == "" {
		req.BotID = "main"
	}
	if req.Timeout <= 0 {
		req.Timeout = 3 * time.Minute
	}
	if err := os.MkdirAll(req.ProfileDir, 0o700); err != nil {
		return BrowserScriptOutcome{}, err
	}

	resKey := host.BrowserProfileResource(req.BotID)
	var lease *host.Lease
	if req.NonBlocking {
		var ok bool
		lease, ok = p.Leases.TryAcquire(resKey, "browser:"+req.ScriptPath, req.Timeout+30*time.Second)
		if !ok {
			return BrowserScriptOutcome{SkippedBusy: true}, nil
		}
	} else {
		var err error
		lease, err = p.Leases.Acquire(ctx, resKey, "browser:"+req.ScriptPath, req.Timeout+30*time.Second)
		if err != nil {
			return BrowserScriptOutcome{}, err
		}
	}
	defer lease.Release()

	guardPath, err := EnsureGuardFile(req.Workspace)
	if err != nil {
		return BrowserScriptOutcome{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd, err := p.CommandFn(runCtx, req, guardPath)
	if err != nil {
		return BrowserScriptOutcome{}, err
	}
	cmd.Dir = req.Workspace
	cmd.Env = append(os.Environ(),
		"FROG_BROWSER_PROFILE="+req.ProfileDir,
		"FROG_PW_GUARD="+guardPath,
		"FROG_BOT_ID="+req.BotID,
	)
	if req.FixedUserAgent != "" {
		cmd.Env = append(cmd.Env, "FROG_USER_AGENT="+req.FixedUserAgent)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return BrowserScriptOutcome{}, fmt.Errorf("冷启浏览器脚本失败: %w", err)
	}

	procKey := fmt.Sprintf("%s:%d", req.BotID, time.Now().UnixNano())
	p.mu.Lock()
	p.procs[procKey] = &activeBrowserProc{botID: req.BotID, cmd: cmd, lastUsed: time.Now(), cancel: cancel}
	p.mu.Unlock()

	waitErr := cmd.Wait()

	// Immediately reap entry on script exit -> 0 MB RAM retained
	p.mu.Lock()
	delete(p.procs, procKey)
	p.mu.Unlock()

	out := BrowserScriptOutcome{
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			out.ExitCode = ee.ExitCode()
		} else {
			out.ExitCode = 1
		}
	}

	// Parse structured organ signals emitted by script
	for _, line := range strings.Split(out.Stdout+"\n"+out.Stderr, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "FROG_SIGNAL:WAITING_HUMAN:"); ok {
			var sig WaitingHumanSignal
			if json.Unmarshal([]byte(strings.TrimSpace(rest)), &sig) == nil {
				out.WaitingHuman = &sig
			}
		} else if rest, ok := strings.CutPrefix(line, "FROG_SIGNAL:SELECTOR_CHANGED:"); ok {
			var sig SelectorBrokenSignal
			if json.Unmarshal([]byte(strings.TrimSpace(rest)), &sig) == nil {
				out.SelectorBroken = &sig
			}
		}
	}
	return out, waitErr
}

// RegisterLongLivedProcess registers an interactive CDP browser process with the idle reaper.
func (p *BrowserPool) RegisterLongLivedProcess(id, botID string, cmd *exec.Cmd, lease *host.Lease, cancel context.CancelFunc) {
	p.mu.Lock()
	p.procs[id] = &activeBrowserProc{
		botID:    botID,
		cmd:      cmd,
		lease:    lease,
		lastUsed: time.Now(),
		cancel:   cancel,
	}
	p.mu.Unlock()
}

// TouchProcess resets the idle timer for an active interactive browser session.
func (p *BrowserPool) TouchProcess(id string) {
	p.mu.Lock()
	if ap := p.procs[id]; ap != nil {
		ap.lastUsed = time.Now()
	}
	p.mu.Unlock()
}

// StopProcess kills an active interactive browser session immediately and frees RAM.
func (p *BrowserPool) StopProcess(id string) {
	p.mu.Lock()
	if ap := p.procs[id]; ap != nil {
		p.killLocked(id, ap)
	}
	p.mu.Unlock()
}

func defaultBrowserCommand(ctx context.Context, req BrowserScriptRequest, guardPath string) (*exec.Cmd, error) {
	ext := strings.ToLower(filepath.Ext(req.ScriptPath))
	switch ext {
	case ".py":
		args := append([]string{req.ScriptPath}, req.Args...)
		return exec.CommandContext(ctx, "python3", args...), nil
	default:
		args := append([]string{req.ScriptPath}, req.Args...)
		return exec.CommandContext(ctx, "node", args...), nil
	}
}
