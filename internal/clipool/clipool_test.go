package clipool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"frogbot/internal/host"
	"frogbot/internal/vault"
)

func TestQuotaManagerSniffCooldownAndFailover(t *testing.T) {
	qm := NewQuotaManager([]string{"codex", "claude", "gemini"}, 2, 15.0)
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	qm.Now = func() time.Time { return now }
	qm.AvailabilityCheck = func(string) bool { return true }

	cli, err := qm.SelectCLI("codex", nil)
	if err != nil || cli != "codex" {
		t.Fatalf("expected codex, got %s err=%v", cli, err)
	}

	// Passively sniff 429 rate limit line from codex output
	if !qm.SniffLine("codex", "HTTP 429 rate limit exceeded, try again in 45 minutes") {
		t.Fatal("expected SniffLine to detect rate limit")
	}
	// Must seamlessly fail over to claude
	cli2, err := qm.SelectCLI("codex", nil)
	if err != nil || cli2 != "claude" {
		t.Fatalf("expected failover to claude, got %s err=%v", cli2, err)
	}

	// Sniff remaining percentage dropping below 15% reserve line -> DEGRADED -> failover to gemini
	qm.SniffLine("claude", "Warning: 10% remaining in your 5h window")
	cli3, err := qm.SelectCLI("codex", nil)
	if err != nil || cli3 != "gemini" {
		t.Fatalf("expected failover to gemini when claude degraded, got %s err=%v", cli3, err)
	}

	// Advance time past codex cooldown -> codex recovers automatically
	now = now.Add(50 * time.Minute)
	cli4, err := qm.SelectCLI("codex", nil)
	if err != nil || cli4 != "codex" {
		t.Fatalf("expected codex recovery after cooldown, got %s err=%v", cli4, err)
	}
}

func TestQuotaManagerPriorityPreemption(t *testing.T) {
	qm := NewQuotaManager([]string{"claude"}, 1, 15.0)
	ctx := context.Background()
	rel1, err := qm.AcquireSlot(ctx, "claude", PriorityUser)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	wg.Add(2)

	// Enqueue Cron first, then User while slot is occupied
	go func() {
		defer wg.Done()
		rel, err := qm.AcquireSlot(ctx, "claude", PriorityCron)
		if err != nil {
			return
		}
		mu.Lock()
		order = append(order, "cron")
		mu.Unlock()
		rel()
	}()
	time.Sleep(20 * time.Millisecond)
	go func() {
		defer wg.Done()
		rel, err := qm.AcquireSlot(ctx, "claude", PriorityUser)
		if err != nil {
			return
		}
		mu.Lock()
		order = append(order, "user")
		mu.Unlock()
		rel()
	}()
	time.Sleep(20 * time.Millisecond)
	rel1()
	wg.Wait()

	if len(order) != 2 || order[0] != "user" || order[1] != "cron" {
		t.Fatalf("expected User to preempt Cron, got order=%v", order)
	}
}

func TestLocalCLIPoolAsyncStreamingCallbackAndCancel(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	logsDir := filepath.Join(tmp, "logs")
	key, _, _ := vault.LoadKey("", tmp)
	vlt, _ := vault.OpenFile(filepath.Join(tmp, "vault.enc"), key)
	_ = vlt.SetWithRule("api_secret", "SecretVal_AlphaBetaGamma99", "test", "test", nil)

	qm := NewQuotaManager([]string{"codex", "claude"}, 2, 15.0)
	qm.AvailabilityCheck = func(string) bool { return true }
	leases := host.NewLeaseManager()
	pool := NewLocalCLIPool(qm, leases, vlt, IsolationOptions{})

	var streamMu sync.Mutex
	var streamed strings.Builder
	pool.OnLogChunk = func(_, _, chunk string) {
		streamMu.Lock()
		streamed.WriteString(chunk)
		streamMu.Unlock()
	}
	cbCh := make(chan CLITaskCallback, 4)
	pool.OnComplete = func(cb CLITaskCallback) { cbCh <- cb }

	// Mock CLI: writes a workspace file, prints >1000 chars + leaks secret (must be scrubbed & <=800 chars!)
	pool.BuildCommand = func(ctx context.Context, cli, mode, prompt, workspace string) (*exec.Cmd, error) {
		if strings.Contains(prompt, "SLEEP_FOREVER") {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "echo starting; sleep 30"), nil
		}
		script := "echo 'fixed' > checkin.py; for i in $(seq 1 40); do echo \"step $i: building module output SecretVal_AlphaBetaGamma99\"; done; echo 'DONE_SUMMARY'"
		return exec.CommandContext(ctx, "/bin/sh", "-c", script), nil
	}

	st, err := pool.Dispatch(context.Background(), CLITaskRequest{
		BotID:      "checkin",
		Mode:       "auto",
		TaskPrompt: "修复签到脚本",
		Workspace:  ws,
		LogsDir:    logsDir,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	select {
	case cb := <-cbCh:
		if cb.Status != "completed" || cb.TaskID != st.ID {
			t.Fatalf("unexpected callback: %+v", cb)
		}
		if utf8.RuneCountInString(cb.Summary) > MaxCallbackSummaryChars {
			t.Fatalf("summary exceeded %d chars: %d", MaxCallbackSummaryChars, utf8.RuneCountInString(cb.Summary))
		}
		if strings.Contains(cb.Summary, "SecretVal_AlphaBetaGamma99") || !strings.Contains(cb.Summary, "[REDACTED:secret://api_secret]") {
			t.Fatalf("summary was not scrubbed: %s", cb.Summary)
		}
		if len(cb.ChangedFiles) != 1 || cb.ChangedFiles[0] != "checkin.py" {
			t.Fatalf("expected checkin.py in ChangedFiles, got %v", cb.ChangedFiles)
		}
		if _, err := os.Stat(cb.LogPath); err != nil {
			t.Fatalf("expected log file on disk: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for CLI completion callback")
	}

	// Verify workspace lease was released
	if _, held := leases.IsHeld(host.WorkspaceWriteResource("checkin")); held {
		t.Fatal("workspace lease should be released after CLI exit")
	}

	// Verify cancel_cli_task terminates runaway process immediately
	st2, err := pool.Dispatch(context.Background(), CLITaskRequest{
		BotID:      "checkin",
		Mode:       "auto",
		TaskPrompt: "SLEEP_FOREVER",
		Workspace:  ws,
		LogsDir:    logsDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := pool.Cancel(st2.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case cb2 := <-cbCh:
		if cb2.Status != "cancelled" {
			t.Fatalf("expected cancelled, got %+v", cb2)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for cancelled CLI callback")
	}
}
