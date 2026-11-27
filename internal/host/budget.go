package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"frogbot/internal/atomicfile"
)

// BudgetStatus captures a bot's live API consumption and circuit-breaker state.
type BudgetStatus struct {
	BotID            string    `json:"bot_id"`
	Date             string    `json:"date"` // YYYY-MM-DD in bot timezone/UTC
	MaxSteps         int       `json:"max_steps"`
	DailyTokenLimit  int64     `json:"daily_token_limit"`
	TokensUsedToday  int64     `json:"tokens_used_today"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TurnsToday       int       `json:"turns_today"`
	BackgroundPaused bool      `json:"background_paused"`
	PausedReason     string    `json:"paused_reason,omitempty"`
	EventRatePerMin  int       `json:"event_rate_per_min"`
	EventTokensAvail float64   `json:"event_tokens_avail"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type tokenBucket struct {
	ratePerSec float64
	burst      float64
	tokens     float64
	lastRefill time.Time
}

func newTokenBucket(perMin, burst int, now time.Time) *tokenBucket {
	if perMin <= 0 {
		perMin = 5
	}
	if burst <= 0 {
		burst = perMin
	}
	return &tokenBucket{
		ratePerSec: float64(perMin) / 60.0,
		burst:      float64(burst),
		tokens:     float64(burst),
		lastRefill: now,
	}
}

func (tb *tokenBucket) allow(now time.Time) (bool, float64) {
	if now.Before(tb.lastRefill) {
		tb.lastRefill = now
	} else if now.After(tb.lastRefill) {
		elapsed := now.Sub(tb.lastRefill).Seconds()
		tb.tokens += elapsed * tb.ratePerSec
		if tb.tokens > tb.burst {
			tb.tokens = tb.burst
		}
		tb.lastRefill = now
	}
	if tb.tokens < 1.0 {
		return false, tb.tokens
	}
	tb.tokens -= 1.0
	return true, tb.tokens
}

type persistedBudget struct {
	Date             string    `json:"date"`
	TokensUsedToday  int64     `json:"tokens_used_today"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TurnsToday       int       `json:"turns_today"`
	BackgroundPaused bool      `json:"background_paused"`
	PausedReason     string    `json:"paused_reason,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// BudgetGuard enforces per-bot LLM API safety limits:
//  1. Max steps per reasoning turn (default 8)
//  2. External event wake-up token bucket rate limiting (e.g. <= 5 emails/min)
//  3. Per-bot daily token hard circuit breaker (auto-pauses background triggers + alerts client)
type BudgetGuard struct {
	BotID  string
	Path   string // optional persistence path (<botDir>/budget.json)
	Now    func() time.Time
	OnTrip func(status BudgetStatus)

	mu     sync.Mutex
	cfg    BotBudgetConfig
	state  persistedBudget
	bucket *tokenBucket
	loc    *time.Location
}

func NewBudgetGuard(botID, persistPath string, cfg BotBudgetConfig, timezone string) (*BudgetGuard, error) {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 8
	}
	if cfg.DailyTokenLimit <= 0 {
		cfg.DailyTokenLimit = 500_000
	}
	if cfg.EventRatePerMin <= 0 {
		cfg.EventRatePerMin = 5
	}
	if cfg.EventBurst <= 0 {
		cfg.EventBurst = cfg.EventRatePerMin
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil || loc == nil {
		loc = time.Local
	}
	now := time.Now()
	g := &BudgetGuard{
		BotID:  botID,
		Path:   persistPath,
		cfg:    cfg,
		loc:    loc,
		bucket: newTokenBucket(cfg.EventRatePerMin, cfg.EventBurst, now),
		state: persistedBudget{
			Date:             now.In(loc).Format("2006-01-02"),
			BackgroundPaused: cfg.BackgroundPaused,
			UpdatedAt:        now,
		},
	}
	if persistPath != "" {
		if err := g.load(); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func (g *BudgetGuard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *BudgetGuard) load() error {
	raw, err := os.ReadFile(g.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pb persistedBudget
	if err := json.Unmarshal(raw, &pb); err != nil {
		return fmt.Errorf("parse budget.json: %w", err)
	}
	today := g.now().In(g.loc).Format("2006-01-02")
	if pb.Date == today {
		g.state = pb
	}
	return nil
}

func (g *BudgetGuard) saveLocked() {
	if g.Path == "" {
		return
	}
	raw, err := json.MarshalIndent(g.state, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(g.Path), 0o700)
	_ = atomicfile.Write(g.Path, raw, 0o600)
}

func (g *BudgetGuard) rollDayLocked(now time.Time) {
	today := now.In(g.loc).Format("2006-01-02")
	if g.state.Date == today {
		return
	}
	g.state = persistedBudget{
		Date:      today,
		UpdatedAt: now,
	}
	g.saveLocked()
}

// MaxSteps returns the configured hard step cap per thinking turn.
func (g *BudgetGuard) MaxSteps() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cfg.MaxSteps <= 0 {
		return 8
	}
	return g.cfg.MaxSteps
}

// UpdateConfig hot-reloads budget limits.
func (g *BudgetGuard) UpdateConfig(cfg BotBudgetConfig) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cfg.MaxSteps > 0 {
		g.cfg.MaxSteps = cfg.MaxSteps
	}
	if cfg.DailyTokenLimit > 0 {
		g.cfg.DailyTokenLimit = cfg.DailyTokenLimit
	}
	if cfg.EventRatePerMin > 0 {
		g.cfg.EventRatePerMin = cfg.EventRatePerMin
		burst := cfg.EventBurst
		if burst <= 0 {
			burst = cfg.EventRatePerMin
		}
		g.cfg.EventBurst = burst
		g.bucket = newTokenBucket(g.cfg.EventRatePerMin, burst, g.now())
	}
}

// AllowBackgroundEvent gates external event wake-ups (emails, cron, webhooks, peer triggers)
// against both the daily token circuit breaker and the per-minute token bucket.
func (g *BudgetGuard) AllowBackgroundEvent(source string) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.rollDayLocked(now)
	if g.state.BackgroundPaused {
		reason := g.state.PausedReason
		if reason == "" {
			reason = "每日 API Token 预算已触顶，后台触发已暂停"
		}
		return false, reason
	}
	if g.cfg.DailyTokenLimit > 0 && g.state.TokensUsedToday >= g.cfg.DailyTokenLimit {
		g.state.BackgroundPaused = true
		g.state.PausedReason = fmt.Sprintf("已达到每日 Token 上限 (%d/%d)", g.state.TokensUsedToday, g.cfg.DailyTokenLimit)
		g.saveLocked()
		return false, g.state.PausedReason
	}
	ok, _ := g.bucket.allow(now)
	if !ok {
		return false, fmt.Sprintf("外部事件触发过快（限流 %d 次/分钟，来源: %s）", g.cfg.EventRatePerMin, source)
	}
	return true, ""
}

// AllowTurn checks whether an LLM turn is allowed (isBackground distinguishes user-initiated chat from background triggers).
func (g *BudgetGuard) AllowTurn(isBackground bool) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.rollDayLocked(now)
	if isBackground && g.state.BackgroundPaused {
		return false, g.state.PausedReason
	}
	if g.cfg.DailyTokenLimit > 0 && g.state.TokensUsedToday >= g.cfg.DailyTokenLimit {
		msg := fmt.Sprintf("Bot %s 今日自有 API Token 消耗已触顶（%d / %d）", g.BotID, g.state.TokensUsedToday, g.cfg.DailyTokenLimit)
		if isBackground {
			g.state.BackgroundPaused = true
			g.state.PausedReason = msg
			g.saveLocked()
			return false, msg
		}
		return false, msg
	}
	return true, ""
}

// RecordUsage records prompt + completion tokens and trips the circuit breaker if the daily cap is crossed.
func (g *BudgetGuard) RecordUsage(promptTokens, completionTokens int64) (BudgetStatus, bool) {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	g.mu.Lock()
	now := g.now()
	g.rollDayLocked(now)
	wasPaused := g.state.BackgroundPaused
	g.state.PromptTokens += promptTokens
	g.state.CompletionTokens += completionTokens
	g.state.TokensUsedToday += promptTokens + completionTokens
	g.state.TurnsToday++
	g.state.UpdatedAt = now
	tripped := false
	if g.cfg.DailyTokenLimit > 0 && g.state.TokensUsedToday >= g.cfg.DailyTokenLimit {
		g.state.BackgroundPaused = true
		g.state.PausedReason = fmt.Sprintf("每日 Token 预算触顶（%d / %d），已自动暂停后台触发", g.state.TokensUsedToday, g.cfg.DailyTokenLimit)
		if !wasPaused {
			tripped = true
		}
	}
	g.saveLocked()
	st := g.statusLocked(now)
	cb := g.OnTrip
	g.mu.Unlock()

	if tripped && cb != nil {
		cb(st)
	}
	return st, tripped
}

// UnpauseBackground clears the background pause flag (e.g. after raising the daily limit).
func (g *BudgetGuard) UnpauseBackground() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state.BackgroundPaused = false
	g.state.PausedReason = ""
	g.state.UpdatedAt = g.now()
	g.saveLocked()
}

// Status returns a snapshot of the current budget & rate-limiter metrics.
func (g *BudgetGuard) Status() BudgetStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.rollDayLocked(now)
	return g.statusLocked(now)
}

func (g *BudgetGuard) statusLocked(now time.Time) BudgetStatus {
	if now.After(g.bucket.lastRefill) {
		elapsed := now.Sub(g.bucket.lastRefill).Seconds()
		g.bucket.tokens += elapsed * g.bucket.ratePerSec
		if g.bucket.tokens > g.bucket.burst {
			g.bucket.tokens = g.bucket.burst
		}
		g.bucket.lastRefill = now
	}
	return BudgetStatus{
		BotID:            g.BotID,
		Date:             g.state.Date,
		MaxSteps:         g.cfg.MaxSteps,
		DailyTokenLimit:  g.cfg.DailyTokenLimit,
		TokensUsedToday:  g.state.TokensUsedToday,
		PromptTokens:     g.state.PromptTokens,
		CompletionTokens: g.state.CompletionTokens,
		TurnsToday:       g.state.TurnsToday,
		BackgroundPaused: g.state.BackgroundPaused,
		PausedReason:     g.state.PausedReason,
		EventRatePerMin:  g.cfg.EventRatePerMin,
		EventTokensAvail: g.bucket.tokens,
		UpdatedAt:        g.state.UpdatedAt,
	}
}

// EstimateTokens gives a conservative mixed Chinese/ASCII token count when providers omit usage stats.
func EstimateTokens(text string) int64 {
	if text == "" {
		return 0
	}
	var cjk, other int64
	for _, r := range text {
		if r > 0x7F {
			cjk++
		} else {
			other++
		}
	}
	// ~1 token per CJK char + ~1 token per 4 ASCII bytes
	n := cjk + (other+3)/4
	if n < 1 {
		n = 1
	}
	return n
}
