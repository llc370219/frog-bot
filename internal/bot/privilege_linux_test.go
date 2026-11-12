//go:build linux

package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"frogbot/internal/onefrog"
	"frogbot/internal/rootd"
	"frogbot/internal/sandbox"
)

// 完整流程：审批卡片 → 输入密码 → rootd 核对并记下一次授权 → 执行一次，第二次不行。
// 测试进程自己充当「frog 主进程」（rootd 只接受它的 PID）；命令以测试用户身份运行，不需要真的 root。
func TestRootApprovalNeedsPasswordAndRunsOnce(t *testing.T) {
	dir := t.TempDir()
	srv := &rootd.Server{StatePath: filepath.Join(dir, "rootd.json"), AuditPath: filepath.Join(dir, "audit.log")}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.MinCost)
	if err := srv.SetHash(string(hash), true); err != nil {
		t.Fatal(err)
	}
	srv.Verify = func(pid, uid int) error {
		if pid != os.Getpid() {
			return errors.New("不是主进程")
		}
		return nil
	}
	sock := filepath.Join(dir, "rootd.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	// 提权要求沙箱已启用：这里只打开开关，不真的执行沙箱命令
	onefrog.SetSandbox("/nonexistent-frogbot", sandbox.Spec{Protect: []string{dir}})
	defer onefrog.SetSandbox("", sandbox.Spec{})
	p := &Privilege{Client: &rootd.Client{Socket: sock}, Sandbox: sandbox.Status{Available: true}, Server: true}
	SetPrivilege(p)
	defer SetPrivilege(nil)
	if v := p.view(ctx); !v.Available {
		t.Fatalf("privilege should be available: %+v", v)
	}

	b := newBot(t, dir, &fakeAI{})
	defer b.Close()
	args, _ := json.Marshal(map[string]string{"command": "echo from-root", "reason": "test"})
	decided := make(chan onefrog.UserDecision, 1)
	go func() {
		d, _ := b.approver.RequestApproval(ctx, onefrog.ApprovalRequest{ID: "appr_root", ToolName: onefrog.RootTool, Args: args,
			ExpiresAt: time.Now().Add(time.Minute)})
		decided <- d
	}()
	waitFor(t, "pending approval", func() bool { return b.isRootApproval("appr_root") })

	if err := b.DecideWith("appr_root", "once", "", 0); err == nil || !strings.Contains(err.Error(), "密码") {
		t.Fatalf("approving without a password must fail, got %v", err)
	}
	if err := b.DecideWith("appr_root", "once", "wrong", 0); err == nil {
		t.Fatal("wrong password accepted")
	}
	if !b.isRootApproval("appr_root") {
		t.Fatal("card must stay pending after a wrong password")
	}
	if _, _, err := p.RunAsRoot(ctx, "echo sneaky", 10, "t"); err == nil {
		t.Fatal("run before approval must fail")
	}
	if err := b.DecideWith("appr_root", "session", "correct-horse", 0); err != nil {
		t.Fatal(err)
	}
	if d := <-decided; !d.Approve || d.Scope != onefrog.ScopeOnce {
		t.Fatalf("root approval must be once-only: %+v", d)
	}
	out, code, err := p.RunAsRoot(ctx, "echo from-root", 10, "t")
	if err != nil || code != 0 || strings.TrimSpace(out) != "from-root" {
		t.Fatalf("run: %q %d %v", out, code, err)
	}
	if _, _, err := p.RunAsRoot(ctx, "echo again", 10, "t"); err == nil {
		t.Fatal("authorization must be used up")
	}
	audit, _ := os.ReadFile(srv.AuditPath)
	if !strings.Contains(string(audit), "echo from-root") {
		t.Fatalf("audit log missing command: %s", audit)
	}
}
