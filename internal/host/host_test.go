package host

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

func TestLayoutAndConfigRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	l := NewLayout(tmp)
	if err := l.EnsureHostDirs(); err != nil {
		t.Fatalf("EnsureHostDirs: %v", err)
	}
	st, err := os.Stat(l.HostDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o700 {
		t.Fatalf("expected HostDir perm 0700, got %04o", perm)
	}

	hcfg := DefaultHostConfig()
	hcfg.NetworkTag = "[家庭宽带IP]"
	if err := SaveHostConfig(l.HostConfigPath(), hcfg); err != nil {
		t.Fatalf("SaveHostConfig: %v", err)
	}
	loadedHost, err := LoadHostConfig(l.HostConfigPath())
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}
	if loadedHost.NetworkTag != "[家庭宽带IP]" || loadedHost.BrowserIdleSec != 180 {
		t.Fatalf("unexpected host config: %+v", loadedHost)
	}

	bl := l.BotDirs("secretary", "", "")
	if err := bl.EnsureBotDirs(); err != nil {
		t.Fatalf("EnsureBotDirs: %v", err)
	}
	bcfg := DefaultBotConfig("秘书")
	bcfg.Budget.MaxSteps = 6
	bcfg.Budget.DailyTokenLimit = 123456
	if err := SaveBotConfig(bl.ConfigPath, bcfg); err != nil {
		t.Fatalf("SaveBotConfig: %v", err)
	}
	loadedBot, err := LoadBotConfig(bl.ConfigPath, "fallback")
	if err != nil {
		t.Fatalf("LoadBotConfig: %v", err)
	}
	if loadedBot.Name != "秘书" || loadedBot.Budget.MaxSteps != 6 || loadedBot.Budget.DailyTokenLimit != 123456 {
		t.Fatalf("unexpected bot config: %+v", loadedBot)
	}
}

func TestLeaseManagerContentionAndExpiry(t *testing.T) {
	lm := NewLeaseManager()
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	lm.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }

	res := WorkspaceWriteResource("bot_a")
	l1, ok := lm.TryAcquire(res, "cli_1", 10*time.Second)
	if !ok || l1 == nil {
		t.Fatal("expected initial TryAcquire to succeed")
	}
	if _, ok2 := lm.TryAcquire(res, "cron_2", 10*time.Second); ok2 {
		t.Fatal("expected second TryAcquire to fail while held")
	}
	// Advance clock past TTL: expired lease must be automatically reclaimed
	mu.Lock()
	now = now.Add(11 * time.Second)
	mu.Unlock()
	l2, ok := lm.TryAcquire(res, "cron_2", 10*time.Second)
	if !ok || l2 == nil || l2.Holder() != "cron_2" {
		t.Fatal("expected expired lease to be reclaimed")
	}
	// Release l2 asynchronously and ensure blocking Acquire wakes up
	go func() {
		time.Sleep(30 * time.Millisecond)
		l2.Release()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	l3, err := lm.Acquire(ctx, res, "cli_3", 10*time.Second)
	if err != nil {
		t.Fatalf("blocking Acquire failed: %v", err)
	}
	if l3.Holder() != "cli_3" {
		t.Fatalf("expected holder cli_3, got %s", l3.Holder())
	}
	l3.Release()
	if active := lm.Active(); len(active) != 0 {
		t.Fatalf("expected 0 active leases, got %d", len(active))
	}
}

func TestBudgetGuardRateLimitAndCircuitBreaker(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/budget.json"
	cfg := BotBudgetConfig{
		MaxSteps:        8,
		DailyTokenLimit: 1000,
		EventRatePerMin: 2,
		EventBurst:      2,
	}
	g, err := NewBudgetGuard("bot_1", path, cfg, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	g.Now = func() time.Time { return now }

	// Token bucket: 2 burst allowed, 3rd blocked until time passes
	if ok, _ := g.AllowBackgroundEvent("imap"); !ok {
		t.Fatal("1st event should pass")
	}
	if ok, _ := g.AllowBackgroundEvent("imap"); !ok {
		t.Fatal("2nd event should pass")
	}
	if ok, _ := g.AllowBackgroundEvent("imap"); ok {
		t.Fatal("3rd immediate event should be rate-limited")
	}
	now = now.Add(35 * time.Second)
	if ok, _ := g.AllowBackgroundEvent("imap"); !ok {
		t.Fatal("event after refill should pass")
	}

	// Daily token cap circuit breaker
	trippedCalled := false
	g.OnTrip = func(st BudgetStatus) { trippedCalled = true }
	_, tripped := g.RecordUsage(400, 300)
	if tripped || trippedCalled {
		t.Fatal("should not trip below limit")
	}
	st, tripped := g.RecordUsage(200, 200)
	if !tripped || !trippedCalled || !st.BackgroundPaused {
		t.Fatalf("expected circuit breaker to trip: tripped=%v called=%v st=%+v", tripped, trippedCalled, st)
	}
	if ok, _ := g.AllowBackgroundEvent("cron"); ok {
		t.Fatal("background events must be blocked once tripped")
	}

	// Persistence across restart
	g2, err := NewBudgetGuard("bot_1", path, cfg, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	g2.Now = func() time.Time { return now }
	if err := g2.load(); err != nil {
		t.Fatal(err)
	}
	if !g2.Status().BackgroundPaused || g2.Status().TokensUsedToday != 1100 {
		t.Fatalf("expected persisted budget state, got %+v", g2.Status())
	}
}

func TestBotActorSequentialAndPriorityAndStateless(t *testing.T) {
	var mu sync.Mutex
	var order []EventType
	done := make(chan struct{})
	gate := make(chan struct{})

	actor := NewBotActor("bot_main", 16, func(ctx context.Context, ev ActorEvent) error {
		if ev.Text == "first" {
			<-gate
		}
		mu.Lock()
		order = append(order, ev.Type)
		n := len(order)
		mu.Unlock()
		if n == 4 {
			close(done)
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actor.Start(ctx)
	defer actor.Close()

	// Enqueue first event (blocks handler), then queue background + high priority user event
	_ = actor.Submit(ActorEvent{Type: EventTaskCallback, Text: "first"})
	time.Sleep(15 * time.Millisecond)
	_ = actor.Submit(ActorEvent{Type: EventPeerMessage, Text: "peer"})
	_ = actor.Submit(ActorEvent{Type: EventUserMessage, Text: "user_urgent"})

	// Also test stateless task reporting condensed callback
	if err := RunStatelessTask(ctx, actor, nil, "imap:check", func(ctx context.Context) (StatelessTaskResult, error) {
		return StatelessTaskResult{ReportToActor: true, Summary: "1封重要邮件", Tainted: true, TaintSources: []string{"email:boss@co.com"}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	close(gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for actor events")
	}
	mu.Lock()
	defer mu.Unlock()
	// 1st was already executing; 2nd MUST be EventUserMessage (prioritized over PeerMessage/TaskCallback)
	if order[1] != EventUserMessage {
		t.Fatalf("expected UserMessage prioritized 2nd, got order=%v", order)
	}
}

func TestHostDoctorSQLiteTrigram(t *testing.T) {
	tmp := t.TempDir()
	l := NewLayout(tmp)
	_ = l.EnsureHostDirs()
	doc := NewHostDoctor(l, DefaultHostConfig())
	rep := doc.Diagnose(context.Background())
	foundFTS := false
	for _, c := range rep.Checks {
		if c.ID == "sqlite_fts5" {
			foundFTS = true
			if c.Status != "ok" {
				t.Fatalf("expected sqlite_fts5 ok, got %+v", c)
			}
		}
	}
	if !foundFTS {
		t.Fatal("missing sqlite_fts5 check in HostDoctor report")
	}
}
