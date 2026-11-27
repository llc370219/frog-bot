// Package host implements the Frog Bot V2 Host Daemon micro-kernel:
// standardized OS-isolated directory layout, Host Doctor, TTL resource lease locks,
// single-goroutine Bot Actor event inboxes with stateless task splitting,
// and per-bot LLM API budget / event-rate circuit breakers.
package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frogbot/internal/atomicfile"
	"gopkg.in/yaml.v3"
)

// Layout defines the standardized V2 Host + Bot directory layout.
// Host-only sensitive artifacts live under HostDir (0700, files 0600).
// Per-bot workspace directories are isolated so low-privilege worker processes
// (frog-worker / bwrap / Landlock) can only access the bot's workspace/.
type Layout struct {
	RootDir string // e.g. /data or DATA_DIR
	HostDir string // <RootDir>/host (0700)
	BotsDir string // <RootDir>/bots
}

// BotLayout holds all standardized paths for a single Bot Actor instance.
type BotLayout struct {
	BotID             string
	BaseDir           string // <RootDir>/bots/<bot_id> (or <RootDir> for legacy main)
	ConfigPath        string // config.yaml
	SoulPath          string // SOUL.md
	MemoryPath        string // MEMORY.md
	HistoryDBPath     string // history.db (SQLite FTS5 trigram)
	BudgetPath        string // budget.json
	SkillsDir         string // skills/
	LogsDir           string // logs/
	BrowserProfileDir string // browser_profile/
	WorkspaceDir      string // workspace/ (writable by frog-worker)
}

// HostConfig is persisted in <HostDir>/host.yaml.
type HostConfig struct {
	NodeName          string            `yaml:"node_name" json:"node_name"`
	NetworkTag        string            `yaml:"network_tag,omitempty" json:"network_tag,omitempty"` // e.g. "[家庭宽带IP]"
	WorkerUser        string            `yaml:"worker_user,omitempty" json:"worker_user,omitempty"` // e.g. "frog-worker"
	CLIFallbackChain  []string          `yaml:"cli_fallback_chain,omitempty" json:"cli_fallback_chain,omitempty"`
	CLIMaxConcurrent  int               `yaml:"cli_max_concurrent,omitempty" json:"cli_max_concurrent,omitempty"`
	CLIReservePercent float64           `yaml:"cli_reserve_percent,omitempty" json:"cli_reserve_percent,omitempty"`
	BrowserIdleSec    int               `yaml:"browser_idle_sec,omitempty" json:"browser_idle_sec,omitempty"`
	GCRetentionDays   int               `yaml:"gc_retention_days,omitempty" json:"gc_retention_days,omitempty"`
	GCMaxLogBytes     int64             `yaml:"gc_max_log_bytes,omitempty" json:"gc_max_log_bytes,omitempty"`
	Push              PushConfig        `yaml:"push,omitempty" json:"push,omitempty"`
	Extra             map[string]string `yaml:"extra,omitempty" json:"extra,omitempty"`
}

// PushConfig configures pluggable offline wake-up notification channels.
type PushConfig struct {
	BarkURL          string `yaml:"bark_url,omitempty" json:"bark_url,omitempty"`
	UnifiedPushURL   string `yaml:"unified_push_url,omitempty" json:"unified_push_url,omitempty"`
	FCMServerKeyRef  string `yaml:"fcm_server_key_ref,omitempty" json:"fcm_server_key_ref,omitempty"`
	FCMDeviceToken   string `yaml:"fcm_device_token,omitempty" json:"fcm_device_token,omitempty"`
	TelegramTokenRef string `yaml:"telegram_token_ref,omitempty" json:"telegram_token_ref,omitempty"`
	TelegramChatID   string `yaml:"telegram_chat_id,omitempty" json:"telegram_chat_id,omitempty"`
	WebhookURL       string `yaml:"webhook_url,omitempty" json:"webhook_url,omitempty"`
}

// BotConfig is persisted in <BotDir>/config.yaml.
type BotConfig struct {
	Name        string          `yaml:"name" json:"name"`
	API         BotAPIConfig    `yaml:"api,omitempty" json:"api,omitempty"`
	Budget      BotBudgetConfig `yaml:"budget,omitempty" json:"budget,omitempty"`
	CLI         BotCLIConfig    `yaml:"cli,omitempty" json:"cli,omitempty"`
	IMAP        *BotIMAPConfig  `yaml:"imap,omitempty" json:"imap,omitempty"`
	DigestTimes []string        `yaml:"digest_times,omitempty" json:"digest_times,omitempty"`
	Timezone    string          `yaml:"timezone,omitempty" json:"timezone,omitempty"`
}

type BotAPIConfig struct {
	BaseURL       string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIKeyRef     string `yaml:"api_key_ref,omitempty" json:"api_key_ref,omitempty"`
	MainModel     string `yaml:"main_model,omitempty" json:"main_model,omitempty"`
	LightModel    string `yaml:"light_model,omitempty" json:"light_model,omitempty"`
	ReviewerModel string `yaml:"reviewer_model,omitempty" json:"reviewer_model,omitempty"`
}

type BotBudgetConfig struct {
	MaxSteps         int   `yaml:"max_steps,omitempty" json:"max_steps,omitempty"`                   // default 8
	DailyTokenLimit  int64 `yaml:"daily_token_limit,omitempty" json:"daily_token_limit,omitempty"`   // default 500000
	EventRatePerMin  int   `yaml:"event_rate_per_min,omitempty" json:"event_rate_per_min,omitempty"` // default 5
	EventBurst       int   `yaml:"event_burst,omitempty" json:"event_burst,omitempty"`               // default 5
	BackgroundPaused bool  `yaml:"background_paused,omitempty" json:"background_paused,omitempty"`
}

type BotCLIConfig struct {
	Preference    string   `yaml:"preference,omitempty" json:"preference,omitempty"`
	FallbackChain []string `yaml:"fallback_chain,omitempty" json:"fallback_chain,omitempty"`
	TimeoutSec    int      `yaml:"timeout_sec,omitempty" json:"timeout_sec,omitempty"`
}

type BotIMAPConfig struct {
	Enabled         bool     `yaml:"enabled" json:"enabled"`
	Host            string   `yaml:"host" json:"host"`
	Port            int      `yaml:"port" json:"port"`
	Username        string   `yaml:"username" json:"username"`
	PasswordRef     string   `yaml:"password_ref" json:"password_ref"` // e.g. {{secret://qq_imap_auth}}
	Mailbox         string   `yaml:"mailbox,omitempty" json:"mailbox,omitempty"`
	Blacklist       []string `yaml:"blacklist,omitempty" json:"blacklist,omitempty"`
	Whitelist       []string `yaml:"whitelist,omitempty" json:"whitelist,omitempty"`
	FilterMarketing bool     `yaml:"filter_marketing" json:"filter_marketing"`
}

// NewLayout builds a standardized V2 Layout rooted at dataRoot.
func NewLayout(dataRoot string) Layout {
	abs, err := filepath.Abs(dataRoot)
	if err == nil {
		dataRoot = abs
	}
	return Layout{
		RootDir: dataRoot,
		HostDir: filepath.Join(dataRoot, "host"),
		BotsDir: filepath.Join(dataRoot, "bots"),
	}
}

func (l Layout) HostConfigPath() string { return filepath.Join(l.HostDir, "host.yaml") }
func (l Layout) VaultPath() string      { return filepath.Join(l.HostDir, "vault.enc") }
func (l Layout) MasterKeyPath() string  { return filepath.Join(l.HostDir, "master.key") }
func (l Layout) TopologyPath() string   { return filepath.Join(l.HostDir, "topology.json") }
func (l Layout) NodeKeyPath() string    { return filepath.Join(l.HostDir, "node_key.json") }
func (l Layout) ExecSocketPath() string { return filepath.Join(l.HostDir, "frog-exec.sock") }
func (l Layout) UserMemoryPath() string { return filepath.Join(l.HostDir, "USER.md") }

// EnsureHostDirs initializes /data/host with strict 0700 permissions.
func (l Layout) EnsureHostDirs() error {
	if err := os.MkdirAll(l.RootDir, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(l.HostDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(l.HostDir, 0o700); err != nil {
		return err
	}
	return os.MkdirAll(l.BotsDir, 0o750)
}

// BotDirs computes the standardized directory paths for a bot.
// If baseOverride or workspaceOverride are provided (e.g. backward compatibility
// with legacy main bot paths), they are honored while keeping all V2 artifacts in place.
func (l Layout) BotDirs(botID, baseOverride, workspaceOverride string) BotLayout {
	if botID == "" {
		botID = "main"
	}
	base := baseOverride
	if base == "" {
		base = filepath.Join(l.BotsDir, botID)
	}
	ws := workspaceOverride
	if ws == "" {
		ws = filepath.Join(base, "workspace")
	}
	return BotLayout{
		BotID:             botID,
		BaseDir:           base,
		ConfigPath:        filepath.Join(base, "config.yaml"),
		SoulPath:          filepath.Join(base, "SOUL.md"),
		MemoryPath:        filepath.Join(base, "MEMORY.md"),
		HistoryDBPath:     filepath.Join(base, "history.db"),
		BudgetPath:        filepath.Join(base, "budget.json"),
		SkillsDir:         filepath.Join(base, "skills"),
		LogsDir:           filepath.Join(base, "logs"),
		BrowserProfileDir: filepath.Join(base, "browser_profile"),
		WorkspaceDir:      ws,
	}
}

// EnsureBotDirs creates all per-bot directories with appropriate isolation permissions.
func (bl BotLayout) EnsureBotDirs() error {
	for _, dir := range []struct {
		p    string
		perm os.FileMode
	}{
		{bl.BaseDir, 0o700},
		{bl.SkillsDir, 0o700},
		{bl.LogsDir, 0o700},
		{bl.BrowserProfileDir, 0o700},
		{bl.WorkspaceDir, 0o755}, // writable by low-privilege worker user
	} {
		if err := os.MkdirAll(dir.p, dir.perm); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir.p, err)
		}
	}
	return nil
}

// DefaultHostConfig returns sensible production defaults for a 2C2G+ server.
func DefaultHostConfig() HostConfig {
	name, _ := os.Hostname()
	if strings.TrimSpace(name) == "" {
		name = "frog-node"
	}
	return HostConfig{
		NodeName:          name,
		WorkerUser:        "frog-worker",
		CLIFallbackChain:  []string{"codex", "claude", "gemini"},
		CLIMaxConcurrent:  2,
		CLIReservePercent: 15.0,
		BrowserIdleSec:    180,
		GCRetentionDays:   7,
		GCMaxLogBytes:     500 << 20, // 500 MiB
	}
}

func LoadHostConfig(path string) (HostConfig, error) {
	cfg := DefaultHostConfig()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse host.yaml: %w", err)
	}
	if len(cfg.CLIFallbackChain) == 0 {
		cfg.CLIFallbackChain = []string{"codex", "claude", "gemini"}
	}
	if cfg.CLIMaxConcurrent <= 0 {
		cfg.CLIMaxConcurrent = 2
	}
	if cfg.CLIReservePercent <= 0 {
		cfg.CLIReservePercent = 15.0
	}
	if cfg.BrowserIdleSec <= 0 {
		cfg.BrowserIdleSec = 180
	}
	if cfg.GCRetentionDays <= 0 {
		cfg.GCRetentionDays = 7
	}
	if cfg.GCMaxLogBytes <= 0 {
		cfg.GCMaxLogBytes = 500 << 20
	}
	return cfg, nil
}

func SaveHostConfig(path string, cfg HostConfig) error {
	raw, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, raw, 0o600)
}

// DefaultBotConfig returns sensible V2 defaults for a single Bot Actor.
func DefaultBotConfig(name string) BotConfig {
	if strings.TrimSpace(name) == "" {
		name = "Frog"
	}
	return BotConfig{
		Name: name,
		Budget: BotBudgetConfig{
			MaxSteps:        8,
			DailyTokenLimit: 500_000,
			EventRatePerMin: 5,
			EventBurst:      5,
		},
		CLI: BotCLIConfig{
			FallbackChain: []string{"codex", "claude", "gemini"},
			TimeoutSec:    600,
		},
		DigestTimes: []string{"08:30", "21:00"},
		Timezone:    "Asia/Shanghai",
	}
}

func LoadBotConfig(path, fallbackName string) (BotConfig, error) {
	cfg := DefaultBotConfig(fallbackName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse bot config.yaml: %w", err)
	}
	if cfg.Budget.MaxSteps <= 0 {
		cfg.Budget.MaxSteps = 8
	}
	if cfg.Budget.DailyTokenLimit <= 0 {
		cfg.Budget.DailyTokenLimit = 500_000
	}
	if cfg.Budget.EventRatePerMin <= 0 {
		cfg.Budget.EventRatePerMin = 5
	}
	if cfg.Budget.EventBurst <= 0 {
		cfg.Budget.EventBurst = cfg.Budget.EventRatePerMin
	}
	if cfg.CLI.TimeoutSec <= 0 {
		cfg.CLI.TimeoutSec = 600
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	return cfg, nil
}

func SaveBotConfig(path string, cfg BotConfig) error {
	raw, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, raw, 0o600)
}
