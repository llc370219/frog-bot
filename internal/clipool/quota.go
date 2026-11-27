// Package clipool implements Frog Bot V2's privilege-dropped official Coding CLI
// scheduling pool (Auto/Plan black-box execution) and passive, non-intrusive
// QuotaManager with priority queues and circuit-breaker failover.
package clipool

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Priority defines preemptive queue order: User > AutoHeal > Cron.
type Priority int

const (
	PriorityCron     Priority = 1
	PriorityAutoHeal Priority = 2
	PriorityUser     Priority = 3
)

type CLIHealth string

const (
	HealthHealthy     CLIHealth = "HEALTHY"
	HealthDegraded    CLIHealth = "DEGRADED"
	HealthCooldown    CLIHealth = "COOLDOWN"
	HealthUnavailable CLIHealth = "UNAVAILABLE"
)

// QuotaStatus describes the passive, non-intrusive quota state for one Coding CLI.
type QuotaStatus struct {
	CLI                   string    `json:"cli"`
	Health                CLIHealth `json:"health"`
	EstimatedRemainingPct float64   `json:"estimated_remaining_pct"`
	ActiveSlots           int       `json:"active_slots"`
	MaxSlots              int       `json:"max_slots"`
	WindowCalls5h         int       `json:"window_calls_5h"`
	DailyCalls            int       `json:"daily_calls"`
	CooldownUntil         time.Time `json:"cooldown_until,omitempty"`
	LastRateLimitMsg      string    `json:"last_rate_limit_msg,omitempty"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type slotWaiter struct {
	priority Priority
	seq      uint64
	ch       chan struct{}
}

type cliQuotaState struct {
	cli             string
	active          int
	maxSlots        int
	waiters         []*slotWaiter
	cooldownUntil   time.Time
	lastRateMsg     string
	sniffedRemPct   float64 // <0 means not explicitly reported by CLI output
	localFileEvents int     // read-only recent 5h session log count
	callTimes       []time.Time
	windowBudget5h  int
	updatedAt       time.Time
}

// QuotaManager tracks Coding CLI concurrency, priority queuing, and passive quota watermarks
// WITHOUT ever reading OAuth tokens or making active calls to undocumented vendor APIs.
type QuotaManager struct {
	FallbackChain      []string
	MaxConcurrent      int
	ReserveThreshold   float64 // default 15.0%
	WindowBudget5h     int     // default 40 tasks per 5h window for local ledger estimation
	Now                func() time.Time
	HomeDir            string
	AvailabilityCheck  func(cli string) bool

	mu     sync.Mutex
	states map[string]*cliQuotaState
	seq    uint64
}

func NewQuotaManager(fallbackChain []string, maxConcurrent int, reservePct float64) *QuotaManager {
	if len(fallbackChain) == 0 {
		fallbackChain = []string{"codex", "claude", "gemini"}
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	if reservePct <= 0 {
		reservePct = 15.0
	}
	home, _ := os.UserHomeDir()
	return &QuotaManager{
		FallbackChain:    append([]string(nil), fallbackChain...),
		MaxConcurrent:    maxConcurrent,
		ReserveThreshold: reservePct,
		WindowBudget5h:   40,
		HomeDir:          home,
		states:           make(map[string]*cliQuotaState),
	}
}

func (q *QuotaManager) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

func (q *QuotaManager) getOrInitLocked(cli string) *cliQuotaState {
	cli = strings.ToLower(strings.TrimSpace(cli))
	st := q.states[cli]
	if st == nil {
		st = &cliQuotaState{
			cli:            cli,
			maxSlots:       q.MaxConcurrent,
			sniffedRemPct:  -1,
			windowBudget5h: q.WindowBudget5h,
			updatedAt:      q.now(),
		}
		q.states[cli] = st
	}
	return st
}

var (
	rateLimitRe  = regexp.MustCompile(`(?i)(429|rate[_\s-]?limit|usage\s+limit|quota\s+exceeded|too\s+many\s+requests|hit\s+your\s+limit)`)
	resetInRe    = regexp.MustCompile(`(?i)(?:try\s+again|resets?|retry)\s+(?:in|after)\s+(\d+)\s*(s|sec|seconds?|m|min|minutes?|h|hr|hours?)`)
	resetAtClock = regexp.MustCompile(`(?i)resets?\s+at\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?`)
	pctRemainRe  = regexp.MustCompile(`(?i)(\d{1,3}(?:\.\d+)?)\s*%\s*(?:remaining|left|available)`)
	pctUsedRe    = regexp.MustCompile(`(?i)(\d{1,3}(?:\.\d+)?)\s*%\s*(?:used|of\s+(?:your\s+)?(?:5h|daily|session)\s+limit)`)
)

// SniffLine passively inspects one line of stdout/stderr from a running Coding CLI process
// to detect rate limits, reset times, or remaining quota percentages with zero active probing.
func (q *QuotaManager) SniffLine(cli, line string) (rateLimited bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.getOrInitLocked(cli)

	if m := pctRemainRe.FindStringSubmatch(line); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 0 && v <= 100 {
			st.sniffedRemPct = v
			st.updatedAt = now
		}
	} else if m := pctUsedRe.FindStringSubmatch(line); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 0 && v <= 100 {
			st.sniffedRemPct = 100.0 - v
			st.updatedAt = now
		}
	}

	if rateLimitRe.MatchString(line) {
		cooldown := parseResetDuration(now, line)
		st.cooldownUntil = now.Add(cooldown)
		st.sniffedRemPct = 0
		st.lastRateMsg = truncateText(line, 200)
		st.updatedAt = now
		return true
	}
	return false
}

func parseResetDuration(now time.Time, line string) time.Duration {
	if m := resetInRe.FindStringSubmatch(line); len(m) == 3 {
		n, _ := strconv.Atoi(m[1])
		unit := strings.ToLower(m[2])
		if n > 0 {
			switch {
			case strings.HasPrefix(unit, "h"):
				return time.Duration(n) * time.Hour
			case strings.HasPrefix(unit, "m"):
				return time.Duration(n) * time.Minute
			default:
				return time.Duration(n) * time.Second
			}
		}
	}
	if m := resetAtClock.FindStringSubmatch(line); len(m) >= 2 {
		hr, _ := strconv.Atoi(m[1])
		min := 0
		if m[2] != "" {
			min, _ = strconv.Atoi(m[2])
		}
		ampm := strings.ToLower(m[3])
		if ampm == "pm" && hr < 12 {
			hr += 12
		} else if ampm == "am" && hr == 12 {
			hr = 0
		}
		if hr >= 0 && hr <= 23 && min >= 0 && min <= 59 {
			target := time.Date(now.Year(), now.Month(), now.Day(), hr, min, 0, 0, now.Location())
			if !target.After(now) {
				target = target.Add(24 * time.Hour)
			}
			d := target.Sub(now)
			if d > 0 && d <= 24*time.Hour {
				return d
			}
		}
	}
	return 30 * time.Minute
}

// RefreshLocalEstimates performs a strictly read-only scan of public local CLI session/log
// directories (never touching credential/auth files) to estimate 5h window pressure.
func (q *QuotaManager) RefreshLocalEstimates() {
	if q.HomeDir == "" {
		return
	}
	now := q.now()
	cutoff := now.Add(-5 * time.Hour)
	dirs := map[string]string{
		"claude": filepath.Join(q.HomeDir, ".claude", "projects"),
		"codex":  filepath.Join(q.HomeDir, ".codex", "sessions"),
		"gemini": filepath.Join(q.HomeDir, ".gemini", "tmp"),
	}
	counts := map[string]int{}
	for cli, dir := range dirs {
		n := 0
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			// Never inspect credential or auth files
			lower := strings.ToLower(d.Name())
			if strings.Contains(lower, "auth") || strings.Contains(lower, "cred") || strings.Contains(lower, "token") {
				return nil
			}
			if info, err := d.Info(); err == nil && info.ModTime().After(cutoff) {
				n++
			}
			if n >= 200 {
				return fs.SkipAll
			}
			return nil
		})
		counts[cli] = n
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for cli, cnt := range counts {
		st := q.getOrInitLocked(cli)
		st.localFileEvents = cnt
		st.updatedAt = now
	}
}

// RecordDispatch logs a CLI invocation into Host's own sliding-window ledger.
func (q *QuotaManager) RecordDispatch(cli string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	st := q.getOrInitLocked(cli)
	st.callTimes = append(pruneCalls(st.callTimes, now.Add(-24*time.Hour)), now)
	st.updatedAt = now
}

// MarkCooldown explicitly puts a CLI into COOLDOWN until resetAt.
func (q *QuotaManager) MarkCooldown(cli string, resetAt time.Time, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.getOrInitLocked(cli)
	st.cooldownUntil = resetAt
	st.lastRateMsg = reason
	st.updatedAt = q.now()
}

// SelectCLI picks the best healthy CLI honoring preference -> fallback_chain,
// skipping CLIs in COOLDOWN or below the ReserveThreshold circuit breaker.
func (q *QuotaManager) SelectCLI(preferred string, customFallback []string) (string, error) {
	chain := buildCandidateChain(preferred, customFallback, q.FallbackChain)
	if len(chain) == 0 {
		return "", errors.New("未配置可用 Coding CLI")
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()

	var degradedFallback string
	for _, cli := range chain {
		if q.AvailabilityCheck != nil && !q.AvailabilityCheck(cli) {
			continue
		}
		status := q.statusLocked(cli, now)
		if status.Health == HealthHealthy {
			return cli, nil
		}
		if status.Health == HealthDegraded && degradedFallback == "" {
			degradedFallback = cli
		}
	}
	if degradedFallback != "" {
		return degradedFallback, nil
	}
	return "", fmt.Errorf("所有候选 CLI (%s) 均处于限流冷却或不可用状态", strings.Join(chain, ", "))
}

// AcquireSlot reserves a concurrency slot for cli obeying strict priority queuing
// (PriorityUser > PriorityAutoHeal > PriorityCron).
func (q *QuotaManager) AcquireSlot(ctx context.Context, cli string, prio Priority) (release func(), err error) {
	q.mu.Lock()
	st := q.getOrInitLocked(cli)
	if st.active < st.maxSlots && len(st.waiters) == 0 {
		st.active++
		q.mu.Unlock()
		return func() { q.releaseSlot(cli) }, nil
	}
	q.seq++
	w := &slotWaiter{priority: prio, seq: q.seq, ch: make(chan struct{})}
	st.waiters = append(st.waiters, w)
	sort.Slice(st.waiters, func(i, j int) bool {
		if st.waiters[i].priority != st.waiters[j].priority {
			return st.waiters[i].priority > st.waiters[j].priority
		}
		return st.waiters[i].seq < st.waiters[j].seq
	})
	q.mu.Unlock()

	select {
	case <-w.ch:
		return func() { q.releaseSlot(cli) }, nil
	case <-ctx.Done():
		q.mu.Lock()
		for i, cand := range st.waiters {
			if cand == w {
				st.waiters = append(st.waiters[:i], st.waiters[i+1:]...)
				q.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		q.mu.Unlock()
		// Slot was handed to us concurrently just as ctx expired; release it cleanly.
		select {
		case <-w.ch:
			q.releaseSlot(cli)
		default:
		}
		return nil, ctx.Err()
	}
}

func (q *QuotaManager) releaseSlot(cli string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.getOrInitLocked(cli)
	if len(st.waiters) > 0 {
		next := st.waiters[0]
		st.waiters = st.waiters[1:]
		close(next.ch)
		return
	}
	if st.active > 0 {
		st.active--
	}
}

// Snapshot returns passive quota & health statuses for all configured CLIs.
func (q *QuotaManager) Snapshot() []QuotaStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	seen := map[string]bool{}
	var names []string
	for _, c := range q.FallbackChain {
		if !seen[c] {
			seen[c] = true
			names = append(names, c)
		}
	}
	for c := range q.states {
		if !seen[c] {
			seen[c] = true
			names = append(names, c)
		}
	}
	out := make([]QuotaStatus, 0, len(names))
	for _, c := range names {
		out = append(out, q.statusLocked(c, now))
	}
	return out
}

func (q *QuotaManager) statusLocked(cli string, now time.Time) QuotaStatus {
	st := q.getOrInitLocked(cli)
	st.callTimes = pruneCalls(st.callTimes, now.Add(-24*time.Hour))
	if !st.cooldownUntil.IsZero() && !now.Before(st.cooldownUntil) {
		st.cooldownUntil = time.Time{}
		if st.sniffedRemPct == 0 {
			st.sniffedRemPct = -1
		}
	}
	cutoff5h := now.Add(-5 * time.Hour)
	calls5h := 0
	for _, ts := range st.callTimes {
		if ts.After(cutoff5h) {
			calls5h++
		}
	}
	budget := st.windowBudget5h
	if budget <= 0 {
		budget = 40
	}
	estUsed := calls5h + st.localFileEvents/4
	estRem := 100.0 * float64(max(0, budget-estUsed)) / float64(budget)
	if st.sniffedRemPct >= 0 && st.sniffedRemPct < estRem {
		estRem = st.sniffedRemPct
	}

	health := HealthHealthy
	if q.AvailabilityCheck != nil && !q.AvailabilityCheck(cli) {
		health = HealthUnavailable
	} else if !st.cooldownUntil.IsZero() && now.Before(st.cooldownUntil) {
		health = HealthCooldown
		estRem = 0
	} else if estRem < q.ReserveThreshold {
		health = HealthDegraded
	}

	return QuotaStatus{
		CLI:                   cli,
		Health:                health,
		EstimatedRemainingPct: estRem,
		ActiveSlots:           st.active,
		MaxSlots:              st.maxSlots,
		WindowCalls5h:         calls5h,
		DailyCalls:            len(st.callTimes),
		CooldownUntil:         st.cooldownUntil,
		LastRateLimitMsg:      st.lastRateMsg,
		UpdatedAt:             st.updatedAt,
	}
}

func pruneCalls(xs []time.Time, cutoff time.Time) []time.Time {
	out := xs[:0]
	for _, t := range xs {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

func buildCandidateChain(preferred string, custom, global []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	add(preferred)
	for _, c := range custom {
		add(c)
	}
	for _, c := range global {
		add(c)
	}
	return out
}

func truncateText(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "…"
}
