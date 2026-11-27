package memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBlockStoreAtomicAndLimitsAndCacheLock(t *testing.T) {
	tmp := t.TempDir()
	mgr, err := NewHermesManager(
		filepath.Join(tmp, "USER.md"),
		filepath.Join(tmp, "MEMORY.md"),
		filepath.Join(tmp, "SOUL.md"),
		filepath.Join(tmp, "skills"),
		filepath.Join(tmp, "history.db"),
		filepath.Join(tmp, "workspace"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	// Concurrent atomic adds to USER.md must never lose entries
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := mgr.ManageMemory("add", "user", "", "用户偏好条目内容")
			if err != nil {
				t.Errorf("concurrent add %d failed: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()
	entries, usage, err := mgr.UserBlock.List()
	if err != nil || len(entries) != 12 {
		t.Fatalf("expected 12 entries after concurrent add, got %d (err=%v, usage=%+v)", len(entries), err, usage)
	}

	// Update & remove by entry ID
	id0 := entries[0].ID
	if _, err := mgr.ManageMemory("update", "user", id0, "更新后的偏好"); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if _, err := mgr.ManageMemory("remove", "user", entries[1].ID, ""); err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	// Prompt cache lock: frozen snapshot must remain byte-identical after mid-session edits
	snap1 := mgr.FrozenSystemPrefix("BASE PROMPT", "node-a: [main]")
	if _, err := mgr.ManageMemory("add", "memory", "", "盘中新写入的记忆"); err != nil {
		t.Fatal(err)
	}
	snap2 := mgr.FrozenSystemPrefix("BASE PROMPT", "node-a: [main]")
	if snap1 != snap2 {
		t.Fatal("frozen system prompt prefix mutated mid-session! Prompt cache would break")
	}
	if !mgr.SnapshotDirty() {
		t.Fatal("expected SnapshotDirty=true after edit")
	}
	snap3 := mgr.RefreshSnapshot("BASE PROMPT", "node-a: [main]")
	if !strings.Contains(snap3, "盘中新写入的记忆") {
		t.Fatal("expected RefreshSnapshot to pick up new memory")
	}

	// Hard limit & >=90% warning check
	small := NewBlockStore("memory", filepath.Join(tmp, "SMALL.md"), 100)
	r1, err := small.Manage("add", "", strings.Repeat("甲", 82))
	if err != nil {
		t.Fatalf("expected under-limit add to succeed: %v", err)
	}
	if r1.Warning == "" {
		t.Fatalf("expected >=90%% warning, usage=%+v", r1.Usage)
	}
	if _, err := small.Manage("add", "", strings.Repeat("乙", 30)); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("expected ErrCapacityExceeded, got %v", err)
	}
}

func TestHistoryFTS5TrigramChineseSearchAndSlidingWindow(t *testing.T) {
	tmp := t.TempDir()
	h, err := OpenHistoryStore(filepath.Join(tmp, "history.db"), func(_ context.Context, text string) (string, error) {
		return "摘要:" + truncateRunes(text, 20), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	ctx := context.Background()
	items := []HistoryItem{
		{ID: "m1", Role: "user", Content: "请帮我检查每周五早上八点半的飞书自动打卡脚本有没有报错", CreatedAt: time.Now().Add(-2 * time.Hour)},
		{ID: "m2", Role: "assistant", Content: "已排查完毕，发现滑块验证码选择器改版，已触发自愈修复并更新技能库", CreatedAt: time.Now().Add(-1 * time.Hour)},
	}
	for _, it := range items {
		if err := h.Archive(ctx, it); err != nil {
			t.Fatalf("Archive: %v", err)
		}
	}

	// Native unspaced Chinese substring trigram search
	res, err := h.Search(ctx, "自动打卡脚本", 5)
	if err != nil || len(res) != 1 || res[0].ID != "m1" {
		t.Fatalf("trigram Chinese search failed: res=%+v err=%v", res, err)
	}
	// Short 2-char Chinese fallback search
	res2, err := h.Search(ctx, "滑块", 5)
	if err != nil || len(res2) != 1 || res2[0].ID != "m2" {
		t.Fatalf("2-char Chinese fallback failed: res=%+v err=%v", res2, err)
	}

	// SlidingWindow keeps 10 rounds (20 items)
	all := make([]int, 25)
	for i := range all {
		all[i] = i
	}
	keep, evicted := SlidingWindow(all, 10)
	if len(keep) != 20 || len(evicted) != 5 {
		t.Fatalf("unexpected sliding window split: keep=%d evicted=%d", len(keep), len(evicted))
	}
}

func TestPointerizerAndReadFileLinesAndSkills(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "workspace")
	p := NewPointerizer(ws, func(_ context.Context, _ string) (string, error) {
		return "日志包含 50 行构建输出，最后一行显示成功。", nil
	})

	shortOut, _, ptr := p.Intercept(context.Background(), "短文本输出")
	if ptr || shortOut != "短文本输出" {
		t.Fatal("short output should not be pointerized")
	}

	var big strings.Builder
	for i := 1; i <= 60; i++ {
		big.WriteString("第 ")
		big.WriteString(strings.Repeat("日志内容", 8))
		big.WriteString("\n")
	}
	stub, relFile, ptr := p.Intercept(context.Background(), big.String())
	if !ptr || relFile == "" || !strings.Contains(stub, relFile) || !strings.Contains(stub, "最后一行显示成功") {
		t.Fatalf("pointerization failed: ptr=%v rel=%s stub=%s", ptr, relFile, stub)
	}

	lines, err := ReadFileLines(ws, relFile, 1, 3)
	if err != nil || !strings.HasPrefix(lines, "1: ") || !strings.Contains(lines, "3: ") {
		t.Fatalf("ReadFileLines failed: err=%v lines=%s", err, lines)
	}
	if _, err := ReadFileLines(ws, "../escape.txt", 1, 5); err == nil {
		t.Fatal("expected path traversal error")
	}

	// SKILL.md round-trip & static index
	sk := NewSkillStore(filepath.Join(tmp, "skills"))
	if _, err := sk.Save("daily_checkin", "自动完成每日网站签到并在改版时自愈", "1. 运行 checkin.py\n2. 验证结果"); err != nil {
		t.Fatalf("Save skill: %v", err)
	}
	loaded, err := sk.Load("daily_checkin")
	if err != nil || !strings.Contains(loaded.StepsMarkdown, "checkin.py") {
		t.Fatalf("Load skill failed: %+v err=%v", loaded, err)
	}
	if idx := sk.StaticIndex(); !strings.Contains(idx, "- daily_checkin: 自动完成每日网站签到") {
		t.Fatalf("unexpected static index: %s", idx)
	}
}
