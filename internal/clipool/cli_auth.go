package clipool

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"frogbot/internal/agy"
	"frogbot/internal/vault"
)

// CLIAuthSpec describes an official CLI's headless authentication capabilities.
type CLIAuthSpec struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	BinNames            []string `json:"-"`
	SupportsDeviceAuth  bool     `json:"supports_device_auth"`
	SupportsOAuthBridge bool     `json:"supports_oauth_bridge"`
	SupportsOAuthToken  bool     `json:"supports_oauth_token"`
	OAuthTokenEnv       string   `json:"oauth_token_env,omitempty"`
	APIKeyEnv           string   `json:"api_key_env"`
	LoginHint           string   `json:"login_hint"`
	InstallHint         string   `json:"install_hint"`
	VaultTokenKey       string   `json:"-"`
	VaultAPIKeyKey      string   `json:"-"`
}

// CLIAuthStatus is returned to iOS / Web for each supported official CLI.
type CLIAuthStatus struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Installed           bool                `json:"installed"`
	Path                string              `json:"path,omitempty"`
	LoggedIn            bool                `json:"logged_in"`
	AuthMethod          string              `json:"auth_method,omitempty"` // "oauth" | "oauth_token" | "api_key"
	AccountHint         string              `json:"account_hint,omitempty"`
	SupportsDeviceAuth  bool                `json:"supports_device_auth"`
	SupportsOAuthBridge bool                `json:"supports_oauth_bridge"`
	SupportsOAuthToken  bool                `json:"supports_oauth_token"`
	OAuthTokenEnv       string              `json:"oauth_token_env,omitempty"`
	APIKeyEnv           string              `json:"api_key_env"`
	LoginHint           string              `json:"login_hint"`
	InstallHint         string              `json:"install_hint"`
	ActiveSession       *CLIAuthSessionView `json:"active_session,omitempty"`
}

// CLIAuthSessionView describes an active or recently finished interactive headless CLI login session.
type CLIAuthSessionView struct {
	ID           string    `json:"id"`
	CLI          string    `json:"cli"`
	Mode         string    `json:"mode"`   // "device" | "oauth"
	Status       string    `json:"status"` // "running" | "waiting_user" | "succeeded" | "failed" | "cancelled"
	AuthURL      string    `json:"auth_url,omitempty"`
	UserCode     string    `json:"user_code,omitempty"`
	NeedsInput   bool      `json:"needs_input"`
	InputPrompt  string    `json:"input_prompt,omitempty"`
	Output       string    `json:"output,omitempty"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type cliAuthSession struct {
	mu          sync.Mutex
	view        CLIAuthSessionView
	cancel      context.CancelFunc
	stdin       io.WriteCloser
	browserFile string
	scriptFile  string
	done        chan struct{}
}

// CLIAuthManager orchestrates official headless CLI logins on a remote VPS/host
// (Device Code, OAuth URL + localhost callback replay / code paste, and encrypted Token/API Key).
// This is strictly for official CLI authentication — completely separate from any reverse proxy!
type CLIAuthManager struct {
	vault              *vault.Vault
	HomeDir            string
	ResolveBinaryFn    func(id string) (string, error)
	ExecCommandFn      func(ctx context.Context, name string, args ...string) *exec.Cmd
	CallbackHTTPClient *http.Client

	mu       sync.Mutex
	sessions map[string]*cliAuthSession
}

var officialCLISpecs = []CLIAuthSpec{
	{
		ID:                  "codex",
		Name:                "OpenAI Codex",
		BinNames:            []string{"codex"},
		SupportsDeviceAuth:  true,
		SupportsOAuthBridge: true,
		SupportsOAuthToken:  false,
		APIKeyEnv:           "OPENAI_API_KEY",
		VaultAPIKeyKey:      "CLI_AUTH_CODEX_API_KEY",
		LoginHint:           "推荐「设备码登录」（codex login --device-auth）；需先在 ChatGPT 网页设置 › 安全中开启设备码登录，也可直接填 OPENAI_API_KEY。",
		InstallHint:         "npm install -g @openai/codex",
	},
	{
		ID:                  "claude",
		Name:                "Claude Code",
		BinNames:            []string{"claude"},
		SupportsDeviceAuth:  false,
		SupportsOAuthBridge: true,
		SupportsOAuthToken:  true,
		OAuthTokenEnv:       "CLAUDE_CODE_OAUTH_TOKEN",
		APIKeyEnv:           "ANTHROPIC_API_KEY",
		VaultTokenKey:       "CLI_AUTH_CLAUDE_OAUTH_TOKEN",
		VaultAPIKeyKey:      "CLI_AUTH_CLAUDE_API_KEY",
		LoginHint:           "支持在手机打开 OAuth 链接后回传授权码/localhost 回调地址，或直接粘贴本机 `claude setup-token` 生成的长效 OAuth Token / ANTHROPIC_API_KEY。",
		InstallHint:         "npm install -g @anthropic-ai/claude-code",
	},
	{
		ID:                  "gemini",
		Name:                "Gemini CLI",
		BinNames:            []string{"gemini"},
		SupportsDeviceAuth:  false,
		SupportsOAuthBridge: true,
		SupportsOAuthToken:  false,
		APIKeyEnv:           "GEMINI_API_KEY",
		VaultAPIKeyKey:      "CLI_AUTH_GEMINI_API_KEY",
		LoginHint:           "点「OAuth 登录」在手机完成 Google 授权后，会跳到打不开的 http://localhost:xxxx/... 页面，复制地址栏完整链接粘贴回来，VPS 将在本机自动完成回调；也支持直接填 GEMINI_API_KEY。",
		InstallHint:         "npm install -g @google/gemini-cli",
	},
	{
		ID:                  "grok",
		Name:                "Grok CLI",
		BinNames:            []string{"grok"},
		SupportsDeviceAuth:  true,
		SupportsOAuthBridge: true,
		SupportsOAuthToken:  false,
		APIKeyEnv:           "XAI_API_KEY",
		VaultAPIKeyKey:      "CLI_AUTH_GROK_API_KEY",
		LoginHint:           "支持 `grok login --device-auth` 设备码登录、OAuth 链接登录，或直接填 xAI Console 的 XAI_API_KEY。",
		InstallHint:         "npm install -g @vibe-kit/grok-cli",
	},
}

func NewCLIAuthManager(v *vault.Vault) *CLIAuthManager {
	home, _ := os.UserHomeDir()
	return &CLIAuthManager{
		vault:              v,
		HomeDir:            home,
		CallbackHTTPClient: &http.Client{Timeout: 12 * time.Second},
		sessions:           make(map[string]*cliAuthSession),
	}
}

func findCLIAuthSpec(id string) (CLIAuthSpec, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, s := range officialCLISpecs {
		if s.ID == id {
			return s, true
		}
	}
	return CLIAuthSpec{}, false
}

func (m *CLIAuthManager) resolveBinary(spec CLIAuthSpec) (string, error) {
	if m.ResolveBinaryFn != nil {
		return m.ResolveBinaryFn(spec.ID)
	}
	if p, err := agy.ResolveAgentBinary(spec.ID); err == nil && p != "" {
		return p, nil
	}
	if p := agy.FindHostBinary(spec.BinNames); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("VPS / 本机未检测到 %s（可执行 %s 安装）", spec.Name, spec.InstallHint)
}

// EnvOverrides returns decrypted official CLI tokens/API keys saved in the host vault,
// ready to be injected into official CLI child processes (never exposed to LLM prompts).
func (m *CLIAuthManager) EnvOverrides() map[string]string {
	out := map[string]string{}
	if m == nil || m.vault == nil {
		return out
	}
	var keys []string
	keyToEnv := map[string]string{}
	for _, spec := range officialCLISpecs {
		if spec.VaultTokenKey != "" && spec.OAuthTokenEnv != "" && m.vault.Has(spec.VaultTokenKey) {
			keys = append(keys, spec.VaultTokenKey)
			keyToEnv[spec.VaultTokenKey] = spec.OAuthTokenEnv
		}
		if spec.VaultAPIKeyKey != "" && spec.APIKeyEnv != "" && m.vault.Has(spec.VaultAPIKeyKey) {
			keys = append(keys, spec.VaultAPIKeyKey)
			keyToEnv[spec.VaultAPIKeyKey] = spec.APIKeyEnv
		}
	}
	if len(keys) == 0 {
		return out
	}
	vals, err := m.vault.Values(keys)
	if err != nil {
		return out
	}
	for k, v := range vals {
		if envName := keyToEnv[k]; envName != "" && strings.TrimSpace(v) != "" {
			out[envName] = strings.TrimSpace(v)
		}
	}
	return out
}

// ListStatuses inspects all 4 supported official CLIs and returns installation & auth state.
func (m *CLIAuthManager) ListStatuses() []CLIAuthStatus {
	out := make([]CLIAuthStatus, 0, len(officialCLISpecs))
	for _, spec := range officialCLISpecs {
		out = append(out, m.statusFor(spec))
	}
	return out
}

func (m *CLIAuthManager) statusFor(spec CLIAuthSpec) CLIAuthStatus {
	binPath, err := m.resolveBinary(spec)
	st := CLIAuthStatus{
		ID:                  spec.ID,
		Name:                spec.Name,
		Installed:           err == nil && binPath != "",
		Path:                binPath,
		SupportsDeviceAuth:  spec.SupportsDeviceAuth,
		SupportsOAuthBridge: spec.SupportsOAuthBridge,
		SupportsOAuthToken:  spec.SupportsOAuthToken,
		OAuthTokenEnv:       spec.OAuthTokenEnv,
		APIKeyEnv:           spec.APIKeyEnv,
		LoginHint:           spec.LoginHint,
		InstallHint:         spec.InstallHint,
	}

	loggedIn, method, hint := m.detectNativeAuth(spec)
	st.LoggedIn = loggedIn
	st.AuthMethod = method
	st.AccountHint = hint

	m.mu.Lock()
	var latest *CLIAuthSessionView
	for _, s := range m.sessions {
		s.mu.Lock()
		v := s.view
		s.mu.Unlock()
		if v.CLI != spec.ID {
			continue
		}
		if latest == nil || v.UpdatedAt.After(latest.UpdatedAt) {
			cp := v
			latest = &cp
		}
	}
	m.mu.Unlock()
	if latest != nil && (latest.Status == "running" || latest.Status == "waiting_user" || time.Since(latest.UpdatedAt) < 5*time.Minute) {
		st.ActiveSession = latest
	}
	return st
}

func (m *CLIAuthManager) detectNativeAuth(spec CLIAuthSpec) (bool, string, string) {
	home := m.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	// 1. Check native official CLI auth files first
	switch spec.ID {
	case "codex":
		p := filepath.Join(home, ".codex", "auth.json")
		if ok, hint := inspectJSONAuthFile(p, []string{"tokens", "access_token", "OPENAI_API_KEY", "auth_mode"}); ok {
			return true, "oauth", firstNonEmpty(hint, "~/.codex/auth.json")
		}
	case "claude":
		for _, rel := range []string{
			filepath.Join(".claude", ".credentials.json"),
			filepath.Join(".claude", "credentials.json"),
		} {
			p := filepath.Join(home, rel)
			if ok, hint := inspectJSONAuthFile(p, []string{"claudeAiOauth", "oauthAccount", "accessToken", "access_token"}); ok {
				return true, "oauth", firstNonEmpty(hint, "~/"+filepath.ToSlash(rel))
			}
		}
		if ok, hint := inspectJSONAuthFile(filepath.Join(home, ".claude.json"), []string{"oauthAccount", "primaryApiKey"}); ok {
			return true, "oauth", firstNonEmpty(hint, "~/.claude.json")
		}
	case "gemini":
		for _, rel := range []string{
			filepath.Join(".gemini", "oauth_creds.json"),
			filepath.Join(".gemini", "google_accounts.json"),
		} {
			p := filepath.Join(home, rel)
			if ok, hint := inspectJSONAuthFile(p, []string{"access_token", "refresh_token", "active"}); ok {
				return true, "oauth", firstNonEmpty(hint, "~/"+filepath.ToSlash(rel))
			}
		}
	case "grok":
		for _, rel := range []string{
			filepath.Join(".grok", "auth.json"),
			filepath.Join(".grok", "user-settings.json"),
		} {
			p := filepath.Join(home, rel)
			if ok, hint := inspectJSONAuthFile(p, []string{"access_token", "token", "apiKey", "api_key"}); ok {
				return true, "oauth", firstNonEmpty(hint, "~/"+filepath.ToSlash(rel))
			}
		}
	}

	// 2. Check saved encrypted OAuth token / API key in Host Vault
	if m.vault != nil {
		if spec.VaultTokenKey != "" && m.vault.Has(spec.VaultTokenKey) {
			return true, "oauth_token", spec.OAuthTokenEnv + "（已加密托管）"
		}
		if spec.VaultAPIKeyKey != "" && m.vault.Has(spec.VaultAPIKeyKey) {
			return true, "api_key", spec.APIKeyEnv + "（已加密托管）"
		}
	}

	// 3. Check process environment variables
	if spec.OAuthTokenEnv != "" && strings.TrimSpace(os.Getenv(spec.OAuthTokenEnv)) != "" {
		return true, "oauth_token", "环境变量 " + spec.OAuthTokenEnv
	}
	if spec.APIKeyEnv != "" && strings.TrimSpace(os.Getenv(spec.APIKeyEnv)) != "" {
		return true, "api_key", "环境变量 " + spec.APIKeyEnv
	}
	return false, "", ""
}

func inspectJSONAuthFile(path string, keys []string) (bool, string) {
	raw, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(raw)) < 2 {
		return false, ""
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil || len(obj) == 0 {
		return false, ""
	}
	matched := false
	for _, k := range keys {
		v, ok := obj[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				matched = true
			}
		case map[string]any:
			if len(t) > 0 {
				matched = true
			}
		default:
			matched = true
		}
	}
	if !matched {
		return false, ""
	}
	// Extract non-sensitive display hint (email/account) if present
	if s, ok := obj["email"].(string); ok && s != "" {
		return true, s
	}
	if s, ok := obj["active"].(string); ok && s != "" {
		return true, s
	}
	if acct, ok := obj["oauthAccount"].(map[string]any); ok {
		if s, ok := acct["emailAddress"].(string); ok && s != "" {
			return true, s
		}
	}
	return true, ""
}

// StartLogin launches an official CLI headless login session (device-auth or OAuth URL bridge).
func (m *CLIAuthManager) StartLogin(ctx context.Context, cliID, mode string) (*CLIAuthSessionView, error) {
	spec, ok := findCLIAuthSpec(cliID)
	if !ok {
		return nil, fmt.Errorf("不支持的官方 CLI：%s（支持 codex / claude / gemini / grok）", cliID)
	}
	exe, err := m.resolveBinary(spec)
	if err != nil {
		return nil, err
	}

	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "auto" {
		if spec.SupportsDeviceAuth {
			mode = "device"
		} else {
			mode = "oauth"
		}
	}
	if mode == "device" && !spec.SupportsDeviceAuth {
		mode = "oauth"
	}

	// Cancel any previous running session for this CLI
	m.cancelCLI(spec.ID)

	sid := "cliauth_" + randomHexID(6)
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	if ctx != nil && ctx.Err() != nil {
		cancel()
		return nil, ctx.Err()
	}

	// Create BROWSER interceptor script so headless CLIs capture the exact OAuth URL instead of failing xdg-open
	browserFile := filepath.Join(os.TempDir(), fmt.Sprintf("frog-cli-url-%s.txt", sid))
	scriptFile := filepath.Join(os.TempDir(), fmt.Sprintf("frog-browser-%s.sh", sid))
	if runtime.GOOS != "windows" {
		scriptBody := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$1\" >> %q\nexit 0\n", browserFile)
		_ = os.WriteFile(scriptFile, []byte(scriptBody), 0o700)
	}

	cmdArgs, inputPrompt := buildLoginCommand(spec.ID, mode)
	cmd := m.buildExecCommand(sctx, exe, cmdArgs, mode == "oauth")
	cmd.Env = buildAuthChildEnv(exe, m.HomeDir, scriptFile)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		cleanupTempFiles(browserFile, scriptFile)
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		cleanupTempFiles(browserFile, scriptFile)
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		cleanupTempFiles(browserFile, scriptFile)
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		cancel()
		cleanupTempFiles(browserFile, scriptFile)
		return nil, fmt.Errorf("启动 %s 登录失败：%w", spec.Name, err)
	}

	now := time.Now()
	sess := &cliAuthSession{
		view: CLIAuthSessionView{
			ID:          sid,
			CLI:         spec.ID,
			Mode:        mode,
			Status:      "running",
			NeedsInput:  mode == "oauth",
			InputPrompt: inputPrompt,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		cancel:      cancel,
		stdin:       stdinPipe,
		browserFile: browserFile,
		scriptFile:  scriptFile,
		done:        make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[sid] = sess
	m.mu.Unlock()

	go m.watchBrowserFile(sctx, sess, browserFile)
	go m.pumpOutput(sess, stdoutPipe)
	go m.pumpOutput(sess, stderrPipe)
	go func() {
		defer close(sess.done)
		defer cleanupTempFiles(browserFile, scriptFile)
		err := cmd.Wait()
		sess.mu.Lock()
		defer sess.mu.Unlock()
		if sess.view.Status == "cancelled" {
			return
		}
		sess.view.UpdatedAt = time.Now()
		if err == nil {
			sess.view.Status = "succeeded"
			sess.view.NeedsInput = false
			sess.view.Error = ""
		} else {
			// Check whether native auth actually succeeded despite non-zero exit (e.g. interactive exit)
			if ok, _, _ := m.detectNativeAuth(spec); ok && sess.view.AuthURL != "" {
				sess.view.Status = "succeeded"
				sess.view.NeedsInput = false
				sess.view.Error = ""
			} else {
				sess.view.Status = "failed"
				sess.view.NeedsInput = false
				sess.view.Error = err.Error()
			}
		}
	}()

	// Wait briefly (up to 1.5s) so device code / auth URL is often already populated in the initial response!
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		sess.mu.Lock()
		ready := sess.view.AuthURL != "" || sess.view.UserCode != "" || sess.view.Status != "running"
		snap := sess.view
		sess.mu.Unlock()
		if ready {
			return &snap, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	sess.mu.Lock()
	snap := sess.view
	sess.mu.Unlock()
	return &snap, nil
}

func buildLoginCommand(cliID, mode string) ([]string, string) {
	switch cliID {
	case "codex":
		if mode == "device" {
			return []string{"login", "--device-auth"}, ""
		}
		return []string{"login"}, "在手机浏览器完成 OpenAI 登录后，如果页面跳转到无法打开的 http://localhost:1455/auth/callback?...，请复制地址栏完整 URL 粘贴到下方由 VPS 自动完成回调。"
	case "claude":
		return []string{"auth", "login"}, "在手机浏览器完成 Anthropic 登录后，复制页面给出的授权码（或打不开的 http://localhost:xxxx/... 回调 URL）粘贴到下方。"
	case "gemini":
		return []string{}, "在手机浏览器完成 Google 登录后，会跳转到打不开的 http://localhost:xxxx/oauth2callback?... 页面，复制地址栏完整 URL 粘贴到下方即可由 VPS 本机自动回放完成登录。"
	case "grok":
		if mode == "device" {
			return []string{"login", "--device-auth"}, ""
		}
		return []string{"login"}, "在手机浏览器完成 xAI 登录后，如跳转到 localhost 回调页面请复制完整 URL 粘贴到下方。"
	default:
		return []string{"login"}, "请粘贴授权码或 localhost 回调地址。"
	}
}

func (m *CLIAuthManager) buildExecCommand(ctx context.Context, exe string, args []string, preferPTY bool) *exec.Cmd {
	if m.ExecCommandFn != nil {
		return m.ExecCommandFn(ctx, exe, args...)
	}
	// On Unix headless hosts, wrapping OAuth login commands in `script` allocates a PTY so
	// interactive CLIs (claude/gemini) print their OAuth URL and accept pasted codes cleanly.
	if preferPTY && runtime.GOOS != "windows" {
		if scriptBin, err := exec.LookPath("script"); err == nil {
			if runtime.GOOS == "linux" {
				quoted := shellJoin(append([]string{exe}, args...))
				return exec.CommandContext(ctx, scriptBin, "-qefc", quoted, "/dev/null")
			}
			if runtime.GOOS == "darwin" {
				all := append([]string{"-q", "/dev/null", exe}, args...)
				return exec.CommandContext(ctx, scriptBin, all...)
			}
		}
	}
	return exec.CommandContext(ctx, exe, args...)
}

func buildAuthChildEnv(exePath, homeDir, browserScript string) []string {
	drop := map[string]bool{
		"FROGBOT_MASTER_KEY": true,
		"FROGBOT_API_KEY":    true,
		"FROGBOT_PASSWORD":   true,
	}
	var out []string
	hasHome := false
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || drop[k] {
			continue
		}
		if k == "BROWSER" || k == "NO_BROWSER" {
			continue
		}
		if k == "HOME" {
			hasHome = true
		}
		out = append(out, kv)
	}
	if !hasHome && homeDir != "" {
		out = append(out, "HOME="+homeDir)
	}
	if browserScript != "" && runtime.GOOS != "windows" {
		out = append(out, "BROWSER="+browserScript)
	}
	out = append(out, "TERM=xterm-256color")
	if exePath != "" {
		dir := filepath.Dir(exePath)
		out = append(out, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return out
}

func (m *CLIAuthManager) watchBrowserFile(ctx context.Context, sess *cliAuthSession, path string) {
	tk := time.NewTicker(200 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.done:
			return
		case <-tk.C:
			raw, err := os.ReadFile(path)
			if err != nil || len(raw) == 0 {
				continue
			}
			for _, line := range strings.Split(string(raw), "\n") {
				u := strings.TrimSpace(line)
				if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
					sess.mu.Lock()
					if sess.view.AuthURL == "" {
						sess.view.AuthURL = u
						if sess.view.Status == "running" {
							sess.view.Status = "waiting_user"
						}
						sess.view.UpdatedAt = time.Now()
					}
					sess.mu.Unlock()
					return
				}
			}
		}
	}
}

var (
	ansiRe       = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	urlRe        = regexp.MustCompile(`https?://[^\s"'<>()]+`)
	deviceCodeRe = regexp.MustCompile(`\b([A-Z0-9]{4,5}-[A-Z0-9]{4,5})\b`)
)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func parseAuthOutputLine(clean string) (authURL, userCode string) {
	if m := urlRe.FindString(clean); m != "" {
		u := strings.TrimRight(m, ".,;:!?])")
		// Ignore generic docs/status URLs when looking for login/auth URLs
		lower := strings.ToLower(u)
		if strings.Contains(lower, "auth") || strings.Contains(lower, "oauth") ||
			strings.Contains(lower, "login") || strings.Contains(lower, "device") ||
			strings.Contains(lower, "accounts.google.com") || strings.Contains(lower, "claude.ai") ||
			strings.Contains(lower, "anthropic.com") || strings.Contains(lower, "openai.com") ||
			strings.Contains(lower, "x.ai") {
			authURL = u
		}
	}
	if m := deviceCodeRe.FindStringSubmatch(clean); len(m) == 2 {
		// Filter out ISO years like 2026-1127 if purely numeric
		cand := m[1]
		hasLetter := false
		for _, r := range cand {
			if r >= 'A' && r <= 'Z' {
				hasLetter = true
				break
			}
		}
		if hasLetter {
			userCode = cand
		}
	}
	return authURL, userCode
}

func (m *CLIAuthManager) pumpOutput(sess *cliAuthSession, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 256*1024)
	for sc.Scan() {
		clean := strings.TrimSpace(stripANSI(sc.Text()))
		if clean == "" {
			continue
		}
		u, code := parseAuthOutputLine(clean)
		sess.mu.Lock()
		if sess.view.Output != "" {
			sess.view.Output += "\n"
		}
		sess.view.Output += clean
		if len(sess.view.Output) > 4000 {
			sess.view.Output = sess.view.Output[len(sess.view.Output)-4000:]
		}
		if u != "" && sess.view.AuthURL == "" {
			sess.view.AuthURL = u
			if sess.view.Status == "running" {
				sess.view.Status = "waiting_user"
			}
		}
		if code != "" && sess.view.UserCode == "" {
			sess.view.UserCode = code
			if sess.view.Status == "running" {
				sess.view.Status = "waiting_user"
			}
		}
		sess.view.UpdatedAt = time.Now()
		sess.mu.Unlock()
	}
}

// GetSession returns the latest snapshot of an interactive login session.
func (m *CLIAuthManager) GetSession(sessionID string) (*CLIAuthSessionView, bool) {
	m.mu.Lock()
	sess := m.sessions[sessionID]
	m.mu.Unlock()
	if sess == nil {
		return nil, false
	}
	sess.mu.Lock()
	snap := sess.view
	sess.mu.Unlock()
	return &snap, true
}

// SubmitInput accepts EITHER:
//  1. A pasted `http://localhost:<port>/...` or `http://127.0.0.1:<port>/...` OAuth callback URL
//     copied from iOS Safari -> strictly validated as loopback and replayed directly on the VPS!
//  2. A raw authorization code (e.g. Claude Code code-paste flow) -> fed directly into CLI stdin.
func (m *CLIAuthManager) SubmitInput(ctx context.Context, sessionID, rawInput string) (*CLIAuthSessionView, error) {
	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		return nil, errors.New("请输入授权码或浏览器回调链接")
	}

	m.mu.Lock()
	sess := m.sessions[sessionID]
	m.mu.Unlock()
	if sess == nil {
		return nil, errors.New("登录会话不存在或已过期，请重新发起登录")
	}

	if loopbackURL, isCallback, err := normalizeLoopbackCallbackURL(rawInput); isCallback {
		if err != nil {
			return nil, err
		}
		client := m.CallbackHTTPClient
		if client == nil {
			client = &http.Client{Timeout: 12 * time.Second}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, loopbackURL, nil)
		if err != nil {
			return nil, fmt.Errorf("构造本机回调请求失败：%w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("本机回放 OAuth 回调失败（请确认登录会话仍在进行中）：%w", err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()

		sess.mu.Lock()
		sess.view.Output += fmt.Sprintf("\n[Frog] 已在本机回放 OAuth 回调（HTTP %d），等待官方 CLI 完成凭证落盘…", resp.StatusCode)
		sess.view.UpdatedAt = time.Now()
		sess.mu.Unlock()

		// Give the CLI a brief moment to finish writing auth files and exit cleanly
		select {
		case <-sess.done:
		case <-time.After(1200 * time.Millisecond):
		}
		sess.mu.Lock()
		snap := sess.view
		sess.mu.Unlock()
		return &snap, nil
	}

	// Otherwise write pasted authorization code directly into the running official CLI stdin
	sess.mu.Lock()
	stdin := sess.stdin
	status := sess.view.Status
	sess.mu.Unlock()
	if status != "running" && status != "waiting_user" {
		return nil, errors.New("该登录会话已结束，请重新发起")
	}
	if stdin == nil {
		return nil, errors.New("当前登录会话不支持交互输入")
	}
	if _, err := io.WriteString(stdin, rawInput+"\n"); err != nil {
		return nil, fmt.Errorf("向官方 CLI 写入授权码失败：%w", err)
	}
	sess.mu.Lock()
	sess.view.Output += "\n[Frog] 已将授权码提交给官方 CLI，正在校验…"
	sess.view.UpdatedAt = time.Now()
	sess.mu.Unlock()

	select {
	case <-sess.done:
	case <-time.After(1000 * time.Millisecond):
	}
	sess.mu.Lock()
	snap := sess.view
	sess.mu.Unlock()
	return &snap, nil
}

// normalizeLoopbackCallbackURL strictly verifies that a pasted callback URL targets ONLY
// http://localhost:<port>/... or http://127.0.0.1:<port>/... (preventing any SSRF!).
func normalizeLoopbackCallbackURL(raw string) (normalized string, isURL bool, err error) {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "", false, nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", true, fmt.Errorf("回调链接格式无效：%w", err)
	}
	if strings.ToLower(u.Scheme) != "http" {
		return "", true, errors.New("仅允许回放本机 http://localhost 或 http://127.0.0.1 回调链接")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" {
		return "", true, errors.New("本机回调链接缺少端口号")
	}
	h := strings.ToLower(strings.TrimSpace(host))
	if h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return "", true, errors.New("安全拦截：回调链接只能指向 localhost / 127.0.0.1")
	}
	u.Host = net.JoinHostPort("127.0.0.1", port)
	return u.String(), true, nil
}

// CancelSession terminates an active CLI login session.
func (m *CLIAuthManager) CancelSession(sessionID string) error {
	m.mu.Lock()
	sess := m.sessions[sessionID]
	m.mu.Unlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	sess.view.Status = "cancelled"
	sess.view.NeedsInput = false
	sess.view.UpdatedAt = time.Now()
	cancel := sess.cancel
	sess.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (m *CLIAuthManager) cancelCLI(cliID string) {
	m.mu.Lock()
	var toCancel []*cliAuthSession
	for _, s := range m.sessions {
		s.mu.Lock()
		if s.view.CLI == cliID && (s.view.Status == "running" || s.view.Status == "waiting_user") {
			toCancel = append(toCancel, s)
		}
		s.mu.Unlock()
	}
	m.mu.Unlock()
	for _, s := range toCancel {
		s.mu.Lock()
		s.view.Status = "cancelled"
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

// SaveCredential stores an official CLI OAuth token (e.g. `claude setup-token`) or official API Key
// into the host's encrypted blind vault, automatically injected when Frog Bot runs that official CLI.
func (m *CLIAuthManager) SaveCredential(cliID, kind, value string) (*CLIAuthStatus, error) {
	spec, ok := findCLIAuthSpec(cliID)
	if !ok {
		return nil, fmt.Errorf("不支持的官方 CLI：%s", cliID)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("凭证内容不能为空")
	}
	if m.vault == nil {
		return nil, errors.New("宿主加密密钥库未初始化")
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		if spec.SupportsOAuthToken && strings.HasPrefix(value, "sk-ant-oat") {
			kind = "oauth_token"
		} else {
			kind = "api_key"
		}
	}

	var vaultKey, desc string
	switch kind {
	case "oauth_token":
		if !spec.SupportsOAuthToken || spec.VaultTokenKey == "" {
			return nil, fmt.Errorf("%s 不支持直接导入 OAuth Token，请改用设备码/OAuth 登录或 API Key", spec.Name)
		}
		vaultKey = spec.VaultTokenKey
		desc = fmt.Sprintf("%s 官方长效 OAuth Token（%s）", spec.Name, spec.OAuthTokenEnv)
	case "api_key":
		vaultKey = spec.VaultAPIKeyKey
		desc = fmt.Sprintf("%s 官方 API Key（%s）", spec.Name, spec.APIKeyEnv)
	default:
		return nil, fmt.Errorf("不支持的凭证类型：%s", kind)
	}

	if err := m.vault.Set(vaultKey, value, desc, nil); err != nil {
		return nil, err
	}

	// Best-effort: for Codex API Key, also write native `~/.codex/auth.json` via `codex login --with-api-key` if installed
	if spec.ID == "codex" && kind == "api_key" {
		if exe, err := m.resolveBinary(spec); err == nil && exe != "" && m.ExecCommandFn == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			cmd := exec.CommandContext(ctx, exe, "login", "--with-api-key")
			cmd.Stdin = strings.NewReader(value + "\n")
			cmd.Env = buildAuthChildEnv(exe, m.HomeDir, "")
			_ = cmd.Run()
			cancel()
		}
	}

	st := m.statusFor(spec)
	return &st, nil
}

// Logout clears saved vault credentials for the official CLI and optionally invokes official logout / clears native files.
func (m *CLIAuthManager) Logout(cliID string, clearNative bool) (*CLIAuthStatus, error) {
	spec, ok := findCLIAuthSpec(cliID)
	if !ok {
		return nil, fmt.Errorf("不支持的官方 CLI：%s", cliID)
	}
	m.cancelCLI(spec.ID)
	if m.vault != nil {
		if spec.VaultTokenKey != "" {
			_ = m.vault.Delete(spec.VaultTokenKey)
		}
		if spec.VaultAPIKeyKey != "" {
			_ = m.vault.Delete(spec.VaultAPIKeyKey)
		}
	}
	if clearNative {
		home := m.HomeDir
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		switch spec.ID {
		case "codex":
			_ = os.Remove(filepath.Join(home, ".codex", "auth.json"))
		case "claude":
			_ = os.Remove(filepath.Join(home, ".claude", ".credentials.json"))
			_ = os.Remove(filepath.Join(home, ".claude", "credentials.json"))
		case "gemini":
			_ = os.Remove(filepath.Join(home, ".gemini", "oauth_creds.json"))
			_ = os.Remove(filepath.Join(home, ".gemini", "google_accounts.json"))
		case "grok":
			_ = os.Remove(filepath.Join(home, ".grok", "auth.json"))
		}
	}
	st := m.statusFor(spec)
	return &st, nil
}

func cleanupTempFiles(paths ...string) {
	for _, p := range paths {
		if p != "" {
			_ = os.Remove(p)
		}
	}
}

func randomHexID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
