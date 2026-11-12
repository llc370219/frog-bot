//go:build unix

package rootd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func newServer(t *testing.T) (*Server, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	s := &Server{StatePath: filepath.Join(dir, "rootd.json"), AuditPath: filepath.Join(dir, "audit.log"), Now: func() time.Time { return now }}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.MinCost)
	if err := s.SetHash(string(hash), true); err != nil {
		t.Fatal(err)
	}
	return s, &now
}

func do(s *Server, r Request) Response { return s.Handle(context.Background(), r) }

func TestRunNeedsAuthorizationEveryTime(t *testing.T) {
	s, _ := newServer(t)
	if r := do(s, Request{Op: "run", Command: "echo hi"}); r.OK || !strings.Contains(r.Error, "授权") {
		t.Fatalf("run without authorize: %+v", r)
	}
	if r := do(s, Request{Op: "authorize"}); r.OK {
		t.Fatal("authorize without password and without window must fail")
	}
	if r := do(s, Request{Op: "authorize", Password: "wrong"}); r.OK {
		t.Fatal("wrong password accepted")
	}
	if r := do(s, Request{Op: "authorize", Password: "correct-horse"}); !r.OK || !r.Status.Pending {
		t.Fatalf("authorize: %+v", r)
	}
	if r := do(s, Request{Op: "run", Command: "echo hi; exit 3"}); !r.OK || strings.TrimSpace(r.Output) != "hi" || r.ExitCode != 3 {
		t.Fatalf("run: %+v", r)
	}
	// 一次授权只能用一次
	if r := do(s, Request{Op: "run", Command: "echo again"}); r.OK {
		t.Fatal("one-shot authorization reused")
	}
}

func TestGrantWindowStillNeedsPerCommandAuthorize(t *testing.T) {
	s, now := newServer(t)
	if r := do(s, Request{Op: "authorize", Password: "correct-horse", Minutes: 600}); !r.OK || r.Status.GrantUntil.Sub(*now) != MaxGrant {
		t.Fatalf("window must be capped at %v: %+v", MaxGrant, r.Status)
	}
	do(s, Request{Op: "run", Command: "true"})
	// 窗口内不用密码，但每条命令都要先 authorize（frog 只在用户点了批准后才调用）
	if r := do(s, Request{Op: "run", Command: "true"}); r.OK {
		t.Fatal("window must not allow run without authorize")
	}
	if r := do(s, Request{Op: "authorize"}); !r.OK {
		t.Fatalf("authorize inside window: %+v", r)
	}
	if r := do(s, Request{Op: "run", Command: "true"}); !r.OK {
		t.Fatalf("run: %+v", r)
	}
	*now = now.Add(MaxGrant + time.Second)
	if r := do(s, Request{Op: "authorize"}); r.OK {
		t.Fatal("window must expire")
	}
	do(s, Request{Op: "authorize", Password: "correct-horse", Minutes: 10})
	do(s, Request{Op: "revoke"})
	if r := do(s, Request{Op: "authorize"}); r.OK {
		t.Fatal("revoke must close the window")
	}
}

func TestLockoutAfterFiveWrongPasswords(t *testing.T) {
	s, now := newServer(t)
	for i := 0; i < 5; i++ {
		do(s, Request{Op: "authorize", Password: "nope"})
	}
	if r := do(s, Request{Op: "authorize", Password: "correct-horse"}); r.OK || r.Status.LockedUntil.IsZero() {
		t.Fatalf("must be locked: %+v", r)
	}
	*now = now.Add(16 * time.Minute)
	if r := do(s, Request{Op: "authorize", Password: "correct-horse"}); !r.OK {
		t.Fatalf("lock must expire: %+v", r)
	}
}

func TestDisableEnableAndPasswordChange(t *testing.T) {
	s, _ := newServer(t)
	do(s, Request{Op: "authorize", Password: "correct-horse", Minutes: 10})
	if r := do(s, Request{Op: "disable"}); !r.OK || r.Status.Enabled || !r.Status.GrantUntil.IsZero() {
		t.Fatalf("disable: %+v", r)
	}
	if r := do(s, Request{Op: "authorize", Password: "correct-horse"}); r.OK {
		t.Fatal("authorize while disabled")
	}
	if r := do(s, Request{Op: "enable", Password: "bad"}); r.OK {
		t.Fatal("enable needs the password")
	}
	if r := do(s, Request{Op: "enable", Password: "correct-horse"}); !r.OK || !r.Status.Enabled {
		t.Fatalf("enable: %+v", r)
	}
	if r := do(s, Request{Op: "set_password", Old: "bad", New: "new-password-1"}); r.OK {
		t.Fatal("set_password needs the old password")
	}
	if r := do(s, Request{Op: "set_password", Old: "correct-horse", New: "new-password-1"}); !r.OK {
		t.Fatalf("set_password: %+v", r)
	}
	// 状态写在磁盘上：重新加载后新密码有效、旧密码无效
	s2 := &Server{StatePath: s.StatePath}
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if r := do(s2, Request{Op: "authorize", Password: "correct-horse"}); r.OK {
		t.Fatal("old password still works")
	}
	if r := do(s2, Request{Op: "authorize", Password: "new-password-1"}); !r.OK {
		t.Fatalf("new password: %+v", r)
	}
}

func TestUnconfiguredRefuses(t *testing.T) {
	s := &Server{StatePath: filepath.Join(t.TempDir(), "none.json")}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if r := do(s, Request{Op: "authorize", Password: "x"}); r.OK {
		t.Fatal("unconfigured rootd authorized")
	}
	if r := do(s, Request{Op: "status"}); !r.OK || r.Status.Configured {
		t.Fatalf("status: %+v", r)
	}
}
