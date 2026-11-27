package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"frogbot/internal/agy"
	"frogbot/internal/atomicfile"
	"frogbot/internal/vault"
)

const (
	ProxyDisclaimerVersion = "v1"
	ProxyChannelID         = "proxy-gateway"
	ProxyDisclaimerText    = "【重要免责声明】\n1. 账号反代网关（如 CLIProxyAPI / OpenCodex / Magpie）通过第三方开源工具将个人订阅会话转为 OpenAI 兼容 API，可能违反 OpenAI / Anthropic / Google / xAI 等服务商的使用条款（ToS），存在账号限流、风控校验或封禁失效的风险。\n2. Frog Bot 仅作为本机渠道对接与进程管理工具，不内置破解、不绕过厂商风控、不提供任何共享账号服务。\n3. 如需稳定合规的长期使用，建议优先在「API 渠道」中直接配置官方或正规服务商的自定义 API Key（可随时与反代渠道并存、按 Bot 自由切换）。\n4. 启用即表示您已充分知悉上述风险，并自行承担使用第三方反代工具产生的一切后果。"
)

// ProxyEnginePreset describes a mature open-source reverse proxy engine supported by Route A.
type ProxyEnginePreset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	BinNames    []string `json:"-"`
	DefaultURL  string   `json:"default_url"`
	DefaultArgs string   `json:"default_args"`
	RepoURL     string   `json:"repo_url"`
	InstallHint string   `json:"install_hint"`
	Description string   `json:"description"`
}

var proxyEnginePresets = []ProxyEnginePreset{
	{
		ID:          "cliproxyapi",
		Name:        "CLIProxyAPI",
		BinNames:    []string{"cli-proxy-api", "cliproxyapi", "CLIProxyAPI"},
		DefaultURL:  "http://127.0.0.1:8317/v1",
		DefaultArgs: "",
		RepoURL:     "https://github.com/router-for-me/CLIProxyAPI",
		InstallHint: "下载 CLIProxyAPI 单二进制并运行，默认监听 http://127.0.0.1:8317/v1",
		Description: "轻量 Go 单二进制，支持将 Codex / Claude / Gemini / Grok 订阅反代为统一 OpenAI 接口",
	},
	{
		ID:          "opencodex",
		Name:        "OpenCodex",
		BinNames:    []string{"opencodex", "ocx"},
		DefaultURL:  "http://127.0.0.1:8787/v1",
		DefaultArgs: "start",
		RepoURL:     "https://github.com/lidge-jun/opencodex",
		InstallHint: "npm install -g @bitkyc08/opencodex && ocx start",
		Description: "面向 Codex / Claude Code 的通用反代网关，支持多账号池化与协议转换",
	},
	{
		ID:          "magpie",
		Name:        "Magpie",
		BinNames:    []string{"magpie"},
		DefaultURL:  "http://127.0.0.1:3425/v1",
		DefaultArgs: "",
		RepoURL:     "https://github.com/yetone/magpie",
		InstallHint: "启动本机 Magpie 网关（默认监听 http://127.0.0.1:3425/v1）",
		Description: "本机多订阅聚合网关，支持配额感知、自动容灾切换",
	},
	{
		ID:          "custom",
		Name:        "自定义反代网关",
		DefaultURL:  "http://127.0.0.1:8317/v1",
		DefaultArgs: "",
		InstallHint: "填写任意已运行的本地或私网 OpenAI 兼容反代地址（/v1）",
		Description: "对接任意自建/第三方本地反代服务（不限引擎）",
	},
}

// ProxyGatewayConfig is persisted to data/host/proxy-gateway.json (API key sealed via hostVault).
type ProxyGatewayConfig struct {
	Enabled              bool     `json:"enabled"`
	DisclaimerAccepted   bool     `json:"disclaimer_accepted"`
	DisclaimerAcceptedAt string   `json:"disclaimer_accepted_at,omitempty"`
	DisclaimerVersion    string   `json:"disclaimer_version,omitempty"`
	Engine               string   `json:"engine"` // cliproxyapi | opencodex | magpie | custom
	BaseURL              string   `json:"base_url"`
	ChannelName          string   `json:"channel_name"`
	AuthRequired         bool     `json:"auth_required"`
	SealedAPIKey         string   `json:"sealed_api_key,omitempty"`
	HasAPIKey            bool     `json:"has_api_key"`
	AutoStart            bool     `json:"auto_start"`
	BinaryPath           string   `json:"binary_path,omitempty"`
	StartArgs            string   `json:"start_args,omitempty"`
	Models               []string `json:"models,omitempty"`
}

// ProxyGatewaySaveInput is the HTTP payload for saving/enabling the reverse proxy gateway.
type ProxyGatewaySaveInput struct {
	Enabled            bool     `json:"enabled"`
	DisclaimerAccepted bool     `json:"disclaimer_accepted"`
	Engine             string   `json:"engine"`
	BaseURL            string   `json:"base_url"`
	ChannelName        string   `json:"channel_name"`
	AuthRequired       bool     `json:"auth_required"`
	APIKey             *string  `json:"api_key,omitempty"` // nil = keep existing; "" = clear
	AutoStart          bool     `json:"auto_start"`
	BinaryPath         string   `json:"binary_path,omitempty"`
	StartArgs          string   `json:"start_args,omitempty"`
	Models             []string `json:"models,omitempty"`
	SyncChannel        bool     `json:"sync_channel"`
}

// ProxyEngineDetected describes a single preset engine's host availability & live port probe.
type ProxyEngineDetected struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Installed   bool   `json:"installed"`
	BinaryPath  string `json:"binary_path,omitempty"`
	DefaultURL  string `json:"default_url"`
	DefaultArgs string `json:"default_args,omitempty"`
	ProbeAlive  bool   `json:"probe_alive"`
	RepoURL     string `json:"repo_url,omitempty"`
	InstallHint string `json:"install_hint,omitempty"`
	Description string `json:"description,omitempty"`
}

// ProxyGatewayStatus is returned by GET /api/v2/proxy/status.
type ProxyGatewayStatus struct {
	Config            ProxyGatewayConfig    `json:"config"`
	DisclaimerVersion string                `json:"disclaimer_version"`
	DisclaimerText    string                `json:"disclaimer_text"`
	DetectedEngines   []ProxyEngineDetected `json:"detected_engines"`
	ManagedRunning    bool                  `json:"managed_running"`
	ManagedPID        int                   `json:"managed_pid,omitempty"`
	LastLogs          []string              `json:"last_logs,omitempty"`
	ProbeOK           bool                  `json:"probe_ok"`
	ProbeLatencyMS    int64                 `json:"probe_latency_ms,omitempty"`
	ProbeError        string                `json:"probe_error,omitempty"`
	LiveModels        []string              `json:"live_models,omitempty"`
	ChannelSynced     bool                  `json:"channel_synced"`
	ChannelID         string                `json:"channel_id"`
}

// ProxyGatewayManager manages optional integration with mature open-source reverse proxies
// (CLIProxyAPI / OpenCodex / Magpie / custom) for the front Bot layer, enforcing a mandatory
// risk disclaimer and coexisting cleanly with user-configured custom API channels.
type ProxyGatewayManager struct {
	path        string
	vault       *vault.Vault
	pool        *channelPool
	httpClient  *http.Client
	findBinFn   func([]string) string
	skipProbes  bool // optional test knob

	mu       sync.Mutex
	cfg      ProxyGatewayConfig
	apiKey   string
	proc     *exec.Cmd
	procStop context.CancelFunc
	logs     []string
}

func NewProxyGatewayManager(hostDir string, v *vault.Vault, pool *channelPool) *ProxyGatewayManager {
	m := &ProxyGatewayManager{
		path:       filepath.Join(hostDir, "proxy-gateway.json"),
		vault:      v,
		pool:       pool,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		findBinFn:  agy.FindHostBinary,
		cfg: ProxyGatewayConfig{
			Engine:      "cliproxyapi",
			BaseURL:     "http://127.0.0.1:8317/v1",
			ChannelName: "账号反代 · CLIProxyAPI",
		},
	}
	_ = m.load()
	return m
}

func (m *ProxyGatewayManager) load() error {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	var cfg ProxyGatewayConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.Engine == "" {
		cfg.Engine = "cliproxyapi"
	}
	if cfg.SealedAPIKey != "" && m.vault != nil {
		if plain, err := m.vault.OpenAPIKey(cfg.SealedAPIKey); err == nil {
			m.apiKey = plain
		}
	}
	cfg.HasAPIKey = m.apiKey != ""
	m.cfg = cfg
	return nil
}

func (m *ProxyGatewayManager) persistLocked() error {
	cp := m.cfg
	cp.HasAPIKey = m.apiKey != ""
	if m.apiKey != "" && m.vault != nil {
		sealed, err := m.vault.SealAPIKey(m.apiKey)
		if err != nil {
			return err
		}
		cp.SealedAPIKey = sealed
	} else {
		cp.SealedAPIKey = ""
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(m.path, data, 0o600)
}

func findProxyPreset(engine string) ProxyEnginePreset {
	engine = strings.ToLower(strings.TrimSpace(engine))
	for _, p := range proxyEnginePresets {
		if p.ID == engine {
			return p
		}
	}
	return proxyEnginePresets[len(proxyEnginePresets)-1]
}

// Status returns the current reverse proxy configuration, disclaimer, live probes, and managed process state.
func (m *ProxyGatewayManager) Status(ctx context.Context) ProxyGatewayStatus {
	m.mu.Lock()
	cfg := m.cfg
	cfg.SealedAPIKey = ""
	cfg.HasAPIKey = m.apiKey != ""
	apiKey := m.apiKey
	running := m.proc != nil && m.proc.Process != nil
	pid := 0
	if running {
		pid = m.proc.Process.Pid
	}
	logs := slices.Clone(m.logs)
	m.mu.Unlock()

	detected := make([]ProxyEngineDetected, 0, len(proxyEnginePresets))
	for _, p := range proxyEnginePresets {
		var binPath string
		if len(p.BinNames) > 0 && m.findBinFn != nil {
			binPath = m.findBinFn(p.BinNames)
		}
		alive := false
		if !m.skipProbes && p.ID != "custom" && p.DefaultURL != "" {
			pctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
			_, _, err := m.probeEndpoint(pctx, p.DefaultURL, "")
			cancel()
			alive = err == nil
		}
		detected = append(detected, ProxyEngineDetected{
			ID:          p.ID,
			Name:        p.Name,
			Installed:   binPath != "",
			BinaryPath:  binPath,
			DefaultURL:  p.DefaultURL,
			DefaultArgs: p.DefaultArgs,
			ProbeAlive:  alive,
			RepoURL:     p.RepoURL,
			InstallHint: p.InstallHint,
			Description: p.Description,
		})
	}

	st := ProxyGatewayStatus{
		Config:            cfg,
		DisclaimerVersion: ProxyDisclaimerVersion,
		DisclaimerText:    ProxyDisclaimerText,
		DetectedEngines:   detected,
		ManagedRunning:    running,
		ManagedPID:        pid,
		LastLogs:          logs,
		ChannelID:         ProxyChannelID,
	}

	if m.pool != nil {
		if ch, ok := m.pool.find(ProxyChannelID); ok && ch.Enabled {
			st.ChannelSynced = true
		}
	}

	if strings.TrimSpace(cfg.BaseURL) != "" && (cfg.Enabled || running) {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		models, latency, err := m.probeEndpoint(pctx, cfg.BaseURL, apiKey)
		cancel()
		if err == nil {
			st.ProbeOK = true
			st.ProbeLatencyMS = latency
			st.LiveModels = models
		} else {
			st.ProbeError = err.Error()
		}
	}
	return st
}

// Save updates the reverse proxy gateway configuration, enforces the mandatory risk disclaimer,
// and optionally syncs it as a dedicated channel in the Bot channel pool without touching custom APIs.
func (m *ProxyGatewayManager) Save(ctx context.Context, in ProxyGatewaySaveInput) (ProxyGatewayStatus, error) {
	m.mu.Lock()
	alreadyAccepted := m.cfg.DisclaimerAccepted && m.cfg.DisclaimerVersion == ProxyDisclaimerVersion
	m.mu.Unlock()

	if in.Enabled && !in.DisclaimerAccepted && !alreadyAccepted {
		return ProxyGatewayStatus{}, errors.New("启用账号反代前必须阅读并勾选同意免责声明")
	}

	preset := findProxyPreset(in.Engine)
	baseURL := strings.TrimSpace(in.BaseURL)
	if baseURL == "" {
		baseURL = preset.DefaultURL
	}
	if in.Enabled && !strings.HasPrefix(strings.ToLower(baseURL), "http://") && !strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		return ProxyGatewayStatus{}, errors.New("反代网关地址必须以 http:// 或 https:// 开头")
	}
	channelName := strings.TrimSpace(in.ChannelName)
	if channelName == "" {
		channelName = "账号反代 · " + preset.Name
	}

	m.mu.Lock()
	m.cfg.Enabled = in.Enabled
	if in.DisclaimerAccepted || alreadyAccepted {
		m.cfg.DisclaimerAccepted = true
		m.cfg.DisclaimerVersion = ProxyDisclaimerVersion
		if m.cfg.DisclaimerAcceptedAt == "" {
			m.cfg.DisclaimerAcceptedAt = time.Now().Format(time.RFC3339)
		}
	}
	m.cfg.Engine = preset.ID
	m.cfg.BaseURL = strings.TrimRight(baseURL, "/")
	m.cfg.ChannelName = channelName
	m.cfg.AuthRequired = in.AuthRequired
	if in.APIKey != nil {
		m.apiKey = strings.TrimSpace(*in.APIKey)
	}
	if !in.AuthRequired {
		m.apiKey = ""
	}
	m.cfg.AutoStart = in.AutoStart
	m.cfg.BinaryPath = strings.TrimSpace(in.BinaryPath)
	m.cfg.StartArgs = strings.TrimSpace(in.StartArgs)
	if len(in.Models) > 0 {
		m.cfg.Models = cleanModelList(in.Models)
	}
	if err := m.persistLocked(); err != nil {
		m.mu.Unlock()
		return ProxyGatewayStatus{}, err
	}
	m.mu.Unlock()

	if in.SyncChannel || !in.Enabled {
		if err := m.SyncChannel(ctx); err != nil {
			// If enabled and probe failed, still allow saving config unless pool write itself failed
			if !in.Enabled {
				return m.Status(ctx), err
			}
		}
	}
	return m.Status(ctx), nil
}

// SyncChannel upserts the dedicated `proxy-gateway` channel into the device channel pool
// WITHOUT removing or modifying any existing custom API channels.
func (m *ProxyGatewayManager) SyncChannel(ctx context.Context) error {
	if m.pool == nil {
		return nil
	}
	m.mu.Lock()
	cfg := m.cfg
	apiKey := m.apiKey
	m.mu.Unlock()

	existing := m.pool.public()
	idx := slices.IndexFunc(existing, func(c APIChannel) bool { return c.ID == ProxyChannelID })

	if !cfg.Enabled {
		if idx < 0 {
			return nil
		}
		existing[idx].Enabled = false
		_, err := m.pool.save(existing)
		return err
	}

	models := slices.Clone(cfg.Models)
	if live, _, err := m.probeEndpoint(ctx, cfg.BaseURL, apiKey); err == nil && len(live) > 0 {
		models = cleanModelList(append(live, models...))
		m.mu.Lock()
		m.cfg.Models = models
		_ = m.persistLocked()
		m.mu.Unlock()
	}

	var keyPtr *string
	if cfg.AuthRequired && apiKey != "" {
		k := apiKey
		keyPtr = &k
	} else if !cfg.AuthRequired {
		empty := ""
		keyPtr = &empty
	}

	ch := APIChannel{
		ID:           ProxyChannelID,
		Name:         firstNonEmptyStr(cfg.ChannelName, "账号反代网关"),
		BaseURL:      cfg.BaseURL,
		Enabled:      true,
		AuthRequired: cfg.AuthRequired,
		APIKey:       keyPtr,
		Models:       models,
		HiddenModels: []string{},
	}
	if idx >= 0 {
		ch.HiddenModels = existing[idx].HiddenModels
		if len(ch.Models) == 0 {
			ch.Models = existing[idx].Models
		}
		existing[idx] = ch
	} else {
		existing = append(existing, ch)
	}
	_, err := m.pool.save(existing)
	return err
}

// Start launches the configured local reverse proxy engine binary (if installed and disclaimer accepted).
func (m *ProxyGatewayManager) Start(ctx context.Context) (ProxyGatewayStatus, error) {
	m.mu.Lock()
	if !m.cfg.DisclaimerAccepted || m.cfg.DisclaimerVersion != ProxyDisclaimerVersion {
		m.mu.Unlock()
		return ProxyGatewayStatus{}, errors.New("启动反代网关前必须先阅读并同意免责声明")
	}
	if m.proc != nil && m.proc.Process != nil {
		m.mu.Unlock()
		return m.Status(ctx), nil
	}
	preset := findProxyPreset(m.cfg.Engine)
	bin := strings.TrimSpace(m.cfg.BinaryPath)
	if bin == "" && len(preset.BinNames) > 0 && m.findBinFn != nil {
		bin = m.findBinFn(preset.BinNames)
	}
	argsStr := strings.TrimSpace(m.cfg.StartArgs)
	if argsStr == "" {
		argsStr = preset.DefaultArgs
	}
	m.mu.Unlock()

	if bin == "" {
		return ProxyGatewayStatus{}, fmt.Errorf("未检测到 %s 可执行文件，请先安装（%s）或在下方填写其路径", preset.Name, preset.InstallHint)
	}
	var args []string
	if argsStr != "" {
		args = strings.Fields(argsStr)
	}

	pctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(pctx, bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return ProxyGatewayStatus{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return ProxyGatewayStatus{}, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return ProxyGatewayStatus{}, fmt.Errorf("启动 %s 失败：%w", preset.Name, err)
	}

	m.mu.Lock()
	m.proc = cmd
	m.procStop = cancel
	m.logs = []string{fmt.Sprintf("[%s] 已启动 %s (PID %d)", time.Now().Format("15:04:05"), filepath.Base(bin), cmd.Process.Pid)}
	m.mu.Unlock()

	go m.captureProcLogs(stdout)
	go m.captureProcLogs(stderr)
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		if m.proc == cmd {
			m.proc = nil
			m.procStop = nil
		}
		if err != nil {
			m.appendLogLocked(fmt.Sprintf("[%s] 进程已退出：%v", time.Now().Format("15:04:05"), err))
		} else {
			m.appendLogLocked(fmt.Sprintf("[%s] 进程已正常停止", time.Now().Format("15:04:05")))
		}
		m.mu.Unlock()
	}()

	// Wait briefly for the local HTTP server to bind
	time.Sleep(600 * time.Millisecond)
	return m.Status(ctx), nil
}

// Stop stops the Frog Bot managed reverse proxy subprocess if running.
func (m *ProxyGatewayManager) Stop(ctx context.Context) ProxyGatewayStatus {
	m.mu.Lock()
	cancel := m.procStop
	m.proc = nil
	m.procStop = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return m.Status(ctx)
}

// Close shuts down any managed reverse proxy subprocess when Frog Bot exits.
func (m *ProxyGatewayManager) Close() {
	m.mu.Lock()
	cancel := m.procStop
	m.proc = nil
	m.procStop = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *ProxyGatewayManager) captureProcLogs(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		m.mu.Lock()
		m.appendLogLocked(line)
		m.mu.Unlock()
	}
}

func (m *ProxyGatewayManager) appendLogLocked(line string) {
	m.logs = append(m.logs, line)
	if len(m.logs) > 40 {
		m.logs = m.logs[len(m.logs)-40:]
	}
}

func (m *ProxyGatewayManager) probeEndpoint(ctx context.Context, baseURL, apiKey string) ([]string, int64, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, 0, errors.New("未填写反代地址")
	}
	target := baseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := m.httpClient
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	latency := time.Since(start).Milliseconds()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, latency, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return nil, latency, nil
	}
	models := make([]string, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			models = append(models, id)
		}
	}
	return cleanModelList(models), latency, nil
}

func cleanModelList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
