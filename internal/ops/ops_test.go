package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiskGCPointersAgeAndCap(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "bots", "main", "workspace")
	logs := filepath.Join(root, "bots", "main", "logs")
	_ = os.MkdirAll(ws, 0o755)
	_ = os.MkdirAll(logs, 0o700)

	now := time.Date(2026, 11, 27, 4, 0, 0, 0, time.UTC)
	writeTimed := func(p string, content string, age time.Duration) {
		_ = os.WriteFile(p, []byte(content), 0o600)
		mt := now.Add(-age)
		_ = os.Chtimes(p, mt, mt)
	}

	writeTimed(filepath.Join(ws, "tmp_old.txt"), "expired pointer", 72*time.Hour)
	writeTimed(filepath.Join(ws, "tmp_fresh.txt"), "fresh pointer", 12*time.Hour)
	writeTimed(filepath.Join(logs, "cli_old.log"), strings.Repeat("a", 500), 10*24*time.Hour)
	writeTimed(filepath.Join(logs, "cli_mid.log"), strings.Repeat("b", 600), 3*24*time.Hour)
	writeTimed(filepath.Join(logs, "cli_new.log"), strings.Repeat("c", 600), 1*24*time.Hour)

	gc := NewDiskGC(root, GCConfig{
		PointerMaxAge: 48 * time.Hour,
		LogMaxAge:     7 * 24 * time.Hour,
		MaxLogBytes:   800, // forces cli_mid.log to be evicted while keeping cli_new.log
		Now:           func() time.Time { return now },
	})
	rep := gc.RunNow()
	if rep.PointersRemoved != 1 || rep.LogsRemovedByAge != 1 || rep.LogsRemovedByCap != 1 {
		t.Fatalf("unexpected GC report: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(ws, "tmp_fresh.txt")); err != nil {
		t.Fatal("fresh pointer should be kept")
	}
	if _, err := os.Stat(filepath.Join(logs, "cli_new.log")); err != nil {
		t.Fatal("newest log should be kept")
	}
}

func TestEncryptedBackupAndRestore(t *testing.T) {
	src := t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "host"), 0o700)
	_ = os.MkdirAll(filepath.Join(src, "bots", "secretary", "skills", "checkin"), 0o700)
	_ = os.MkdirAll(filepath.Join(src, "bots", "secretary", "browser_profile", "Default"), 0o700)
	_ = os.MkdirAll(filepath.Join(src, "bots", "secretary", "browser_profile", "Cache"), 0o700)

	_ = os.WriteFile(filepath.Join(src, "host", "vault.enc"), []byte(`{"encrypted":"data"}`), 0o600)
	_ = os.WriteFile(filepath.Join(src, "host", "USER.md"), []byte("[#a1b2c3] 喜欢简短回复\n"), 0o600)
	_ = os.WriteFile(filepath.Join(src, "bots", "secretary", "MEMORY.md"), []byte("[#d4e5f6] 每天8点打卡\n"), 0o600)
	_ = os.WriteFile(filepath.Join(src, "bots", "secretary", "skills", "checkin", "SKILL.md"), []byte("# checkin"), 0o600)
	_ = os.WriteFile(filepath.Join(src, "bots", "secretary", "browser_profile", "Default", "Cookies"), []byte("sqlite-cookies"), 0o600)
	_ = os.WriteFile(filepath.Join(src, "bots", "secretary", "browser_profile", "Cache", "junk.bin"), []byte("should-skip"), 0o600)

	archive := filepath.Join(t.TempDir(), "migration.frogbak")
	man, err := Backup(src, archive, "SuperPass#2026")
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if man.Files != 5 {
		t.Fatalf("expected 5 backed-up files (skipping Cache/), got %+v", man)
	}

	// Wrong passphrase must fail GCM auth
	dst := t.TempDir()
	if _, err := Restore(archive, dst, "WrongPass"); err == nil {
		t.Fatal("expected Restore with wrong passphrase to fail")
	}

	// Correct passphrase resurrects everything intact
	rman, err := Restore(archive, dst, "SuperPass#2026")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rman.Files != 5 {
		t.Fatalf("expected 5 restored files, got %+v", rman)
	}
	gotMem, err := os.ReadFile(filepath.Join(dst, "bots", "secretary", "MEMORY.md"))
	if err != nil || !strings.Contains(string(gotMem), "每天8点打卡") {
		t.Fatalf("restored MEMORY.md mismatch: %q err=%v", string(gotMem), err)
	}
	if _, err := os.Stat(filepath.Join(dst, "bots", "secretary", "browser_profile", "Default", "Cookies")); err != nil {
		t.Fatal("expected browser profile Cookies restored")
	}
	if _, err := os.Stat(filepath.Join(dst, "bots", "secretary", "browser_profile", "Cache", "junk.bin")); err == nil {
		t.Fatal("ephemeral browser Cache should NOT be included in backup")
	}
}
