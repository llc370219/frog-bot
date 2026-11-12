//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 测试二进制自己充当 frogbot：被 Wrap 改写后以 sandbox-exec / sandbox-check 子命令运行。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sandbox-exec":
			Main(os.Args[2:])
			return
		case "sandbox-check":
			CheckMain(os.Args[2:])
			return
		}
	}
	os.Exit(m.Run())
}

func requireLandlock(t *testing.T) {
	t.Helper()
	st := Probe()
	if !st.Available {
		// CI 设了 FROG_REQUIRE_LANDLOCK=1：沙箱测试不能悄悄跳过
		if os.Getenv("FROG_REQUIRE_LANDLOCK") == "1" {
			t.Fatal("Landlock 不可用：" + st.Reason)
		}
		t.Skip("Landlock 不可用：" + st.Reason)
	}
	t.Logf("Landlock ABI %d", st.ABI)
}

func TestLandlockBlocksDataDirButNotWorkspace(t *testing.T) {
	requireLandlock(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	data := filepath.Join(root, "data")
	tools := filepath.Join(data, "tools")
	ws := filepath.Join(root, "workspace")
	for _, d := range []string{tools, ws} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(data, "auth.json")
	os.WriteFile(secret, []byte(`{"session_secret":"s3cret"}`), 0o600)
	os.WriteFile(filepath.Join(tools, "cli.txt"), []byte("tool"), 0o644)
	spec := Spec{Protect: []string{data}, Allow: []Rule{{tools, ReadOnly}, {ws, ReadWrite}}}

	run := func(script string) (string, error) {
		cmd := exec.Command("/bin/sh", "-c", script)
		if err := Wrap(cmd, self, spec); err != nil {
			t.Fatal(err)
		}
		cmd.Dir = ws
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("cat " + secret); err == nil || strings.Contains(out, "s3cret") {
		t.Fatalf("sandbox must not read %s: %q %v", secret, out, err)
	}
	if out, err := run("echo x > " + filepath.Join(data, "auth.json")); err == nil {
		t.Fatalf("sandbox must not overwrite data: %q", out)
	}
	if out, err := run("rm -f " + secret); err == nil {
		if _, serr := os.Stat(secret); serr != nil {
			t.Fatalf("sandbox deleted protected file: %q", out)
		}
	}
	if out, err := run("cat " + filepath.Join(tools, "cli.txt")); err != nil || out != "tool" {
		t.Fatalf("tools must stay readable: %q %v", out, err)
	}
	if out, err := run("echo y > " + filepath.Join(tools, "new.txt")); err == nil {
		t.Fatalf("tools must be read-only: %q", out)
	}
	if out, err := run("echo hello > note.txt && cat note.txt && mkdir -p sub && mv note.txt sub/ && cat /etc/hostname >/dev/null"); err != nil || !strings.HasPrefix(out, "hello") {
		t.Fatalf("workspace and the rest of the system must work: %q %v", out, err)
	}
	// 子进程也在沙箱里（不能靠再起一个进程绕过）
	if out, err := run("sh -c 'cat " + secret + "'"); err == nil || strings.Contains(out, "s3cret") {
		t.Fatalf("grandchild escaped sandbox: %q", out)
	}
	if err := SelfTest(self, spec); err != nil {
		t.Fatalf("self test: %v", err)
	}
}

func TestSelfTestFailsWithoutProtection(t *testing.T) {
	requireLandlock(t)
	self, _ := os.Executable()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	data := filepath.Join(root, "data")
	os.MkdirAll(data, 0o755)
	// 把受保护目录本身也放进可读写例外：自检必须发现能读到
	spec := Spec{Protect: []string{data}, Allow: []Rule{{data, ReadWrite}}}
	if err := SelfTest(self, spec); err == nil {
		t.Fatal("self test must fail when the protected dir is readable")
	}
}
