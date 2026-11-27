package organs

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"frogbot/internal/atomicfile"
)

// JitterSpec parses 5-field cron plus optional anti-bot random jitter window:
// e.g. "0 8 * * * ~15m" triggers daily at 08:00 + random offset in [0, 15m).
type JitterSpec struct {
	Raw      string        `json:"raw"`
	Minutes  []int         `json:"-"`
	Hours    []int         `json:"-"`
	DOM      []int         `json:"-"`
	Months   []int         `json:"-"`
	DOW      []int         `json:"-"`
	Jitter   time.Duration `json:"jitter"`
	Timezone string        `json:"timezone"`
}

func ParseJitterCron(expr, timezone string) (JitterSpec, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return JitterSpec{}, errors.New("cron 表达式不能为空")
	}
	var jitter time.Duration
	fields := strings.Fields(raw)
	if len(fields) == 6 && strings.HasPrefix(fields[5], "~") {
		d, err := time.ParseDuration(strings.TrimPrefix(fields[5], "~"))
		if err != nil || d < 0 || d > 6*time.Hour {
			return JitterSpec{}, fmt.Errorf("无效抖动窗口 %s: %v", fields[5], err)
		}
		jitter = d
		fields = fields[:5]
	}
	if len(fields) != 5 {
		return JitterSpec{}, fmt.Errorf("cron 必须为 5 段（可选末尾 ~15m 抖动），收到: %s", raw)
	}
	minSet, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return JitterSpec{}, err
	}
	hourSet, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return JitterSpec{}, err
	}
	domSet, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return JitterSpec{}, err
	}
	monSet, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return JitterSpec{}, err
	}
	dowSet, err := parseCronField(fields[4], 0, 7)
	if err != nil {
		return JitterSpec{}, err
	}
	// Normalize Sunday 7 -> 0
	for i := range dowSet {
		if dowSet[i] == 7 {
			dowSet[i] = 0
		}
	}
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	return JitterSpec{
		Raw:      raw,
		Minutes:  minSet,
		Hours:    hourSet,
		DOM:      domSet,
		Months:   monSet,
		DOW:      dowSet,
		Jitter:   jitter,
		Timezone: timezone,
	}, nil
}

// NextFire computes the next nominal slot after `after` plus a cryptographically random
// anti-bot jitter offset in [0, Jitter).
func (s JitterSpec) NextFire(after time.Time) (nominal, jittered time.Time, err error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil || loc == nil {
		loc = time.Local
	}
	cur := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := cur.Add(366 * 24 * time.Hour)
	for cur.Before(limit) {
		if !containsInt(s.Months, int(cur.Month())) {
			cur = time.Date(cur.Year(), cur.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !containsInt(s.DOM, cur.Day()) || !containsInt(s.DOW, int(cur.Weekday())) {
			cur = time.Date(cur.Year(), cur.Month(), cur.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if !containsInt(s.Hours, cur.Hour()) {
			cur = cur.Add(time.Hour).Truncate(time.Hour)
			continue
		}
		if !containsInt(s.Minutes, cur.Minute()) {
			cur = cur.Add(time.Minute)
			continue
		}
		nominal = cur
		offset := randomDuration(s.Jitter)
		return nominal, nominal.Add(offset), nil
	}
	return time.Time{}, time.Time{}, errors.New("未找到未来一年内的触发时间")
}

func randomDuration(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	v := binary.LittleEndian.Uint64(b[:])
	return time.Duration(v % uint64(max))
}

func parseCronField(field string, minVal, maxVal int) ([]int, error) {
	seen := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("无效步长: %s", part)
			}
			step = n
			part = base
		}
		lo, hi := minVal, maxVal
		switch {
		case part == "*" || part == "":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			var err1, err2 error
			lo, err1 = strconv.Atoi(a)
			hi, err2 = strconv.Atoi(b)
			if err1 != nil || err2 != nil || lo > hi {
				return nil, fmt.Errorf("无效范围: %s", part)
			}
		default:
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("无效数值: %s", part)
			}
			lo, hi = v, v
		}
		if lo < minVal || hi > maxVal {
			return nil, fmt.Errorf("数值越界 [%d,%d]: %s", minVal, maxVal, field)
		}
		for v := lo; v <= hi; v += step {
			seen[v] = true
		}
	}
	out := make([]int, 0, len(seen))
	for v := minVal; v <= maxVal; v++ {
		if seen[v] {
			out = append(out, v)
		}
	}
	return out, nil
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// RunWithRetry executes a scheduled job with exponential backoff retry on transient errors.
func RunWithRetry(ctx context.Context, maxRetries int, baseBackoff time.Duration, fn func(ctx context.Context, attempt int) error) error {
	if maxRetries < 0 {
		maxRetries = 0
	}
	if baseBackoff <= 0 {
		baseBackoff = 5 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := fn(ctx, attempt); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == maxRetries {
			break
		}
		delay := baseBackoff << attempt
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}

// DigestItem is a non-urgent routine receipt batched into the morning/evening briefing.
type DigestItem struct {
	BotID     string    `json:"bot_id"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// DigestQueue persists routine success receipts and merges them into a single scheduled briefing.
type DigestQueue struct {
	path string
	mu   sync.Mutex
	data struct {
		Items       []DigestItem `json:"items"`
		LastFlushed string       `json:"last_flushed,omitempty"` // YYYY-MM-DD HH:MM slot key
	}
}

func OpenDigestQueue(path string) (*DigestQueue, error) {
	dq := &DigestQueue{path: path}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &dq.data)
	}
	return dq, nil
}

func (q *DigestQueue) Push(item DigestItem) error {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.data.Items = append(q.data.Items, item)
	if len(q.data.Items) > 200 {
		q.data.Items = q.data.Items[len(q.data.Items)-200:]
	}
	return q.saveLocked()
}

func (q *DigestQueue) Pending() []DigestItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]DigestItem(nil), q.data.Items...)
}

// Flush consolidates all pending items into one briefing message and clears the queue.
func (q *DigestQueue) Flush(slotKey string) (briefing string, count int, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.data.Items) == 0 {
		return "", 0, nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## 每日简报汇总（共 %d 项常规执行）\n", len(q.data.Items))
	for _, it := range q.data.Items {
		fmt.Fprintf(&sb, "- [%s] **%s**：%s\n", it.CreatedAt.Format("15:04"), it.Title, strings.TrimSpace(it.Detail))
	}
	count = len(q.data.Items)
	q.data.Items = nil
	if slotKey != "" {
		q.data.LastFlushed = slotKey
	}
	if err := q.saveLocked(); err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(sb.String()), count, nil
}

// FlushIfDue checks if current local HH:MM matches any configured DigestTimes slot not yet flushed today.
func (q *DigestQueue) FlushIfDue(now time.Time, digestTimes []string, timezone string) (string, int, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil || loc == nil {
		loc = time.Local
	}
	localNow := now.In(loc)
	hhmm := localNow.Format("15:04")
	slotKey := localNow.Format("2006-01-02 ") + hhmm
	matched := false
	for _, t := range digestTimes {
		if strings.TrimSpace(t) == hhmm {
			matched = true
			break
		}
	}
	if !matched {
		return "", 0, nil
	}
	q.mu.Lock()
	already := q.data.LastFlushed == slotKey
	q.mu.Unlock()
	if already {
		return "", 0, nil
	}
	return q.Flush(slotKey)
}

func (q *DigestQueue) saveLocked() error {
	if q.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(q.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(q.path, raw, 0o600)
}
