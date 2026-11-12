//go:build !windows

package onefrog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 模拟 systemd 下的服务用户：PATH 只有系统目录，CLI 用 nvm 装在家目录里。
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NVM_DIR", "")
	t.Setenv("PATH", "/usr/bin:/bin")
	return home
}

func writeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCLIFindsNvmInstallOutsidePath(t *testing.T) {
	home := fakeHome(t)
	// 新版本目录里没有 pi（例如 Node 跑不起来后退回旧版本），应该继续往下找到有 pi 的版本。
	if err := os.MkdirAll(filepath.Join(home, ".nvm/versions/node/v26.13.1/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".nvm/versions/node/v22.23.5/bin/pi")
	writeExe(t, want, "#!/bin/sh\n")
	writeExe(t, filepath.Join(home, ".nvm/versions/node/v9.1.0/bin/pi"), "#!/bin/sh\n")
	got, _, err := resolveCLI("pi")
	if err != nil || got != want {
		t.Fatalf("resolveCLI(pi) = %q, %v; want %q", got, err, want)
	}
	if status := DetectCLIs(); !status[2].Installed || status[2].ID != "pi" {
		t.Fatalf("pi should be detected: %+v", status[2])
	}
	if _, _, err := resolveCLI("codex"); err == nil {
		t.Fatal("codex is not installed and must not be found")
	}
}

func TestResolveCLIUsesNpmrcPrefixAndSkipsNonExecutable(t *testing.T) {
	home := fakeHome(t)
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte("fund=false\nprefix=~/npm-root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local/bin/claude"), []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "npm-root/bin/claude")
	writeExe(t, want, "#!/bin/sh\n")
	if got, _, err := resolveCLI("claude"); err != nil || got != want {
		t.Fatalf("resolveCLI(claude) = %q, %v; want %q", got, err, want)
	}
}

func TestWithPathDir(t *testing.T) {
	env := withPathDir([]string{"HOME=/h", "PATH=/usr/bin:/bin"}, "/n/bin")
	if env[1] != "PATH=/n/bin:/usr/bin:/bin" {
		t.Fatalf("got %v", env)
	}
	if again := withPathDir(env, "/usr/bin"); again[1] != env[1] {
		t.Fatalf("dir already on PATH must not be added twice: %v", again)
	}
	if added := withPathDir([]string{"HOME=/h"}, "/n/bin"); added[1] != "PATH=/n/bin" {
		t.Fatalf("got %v", added)
	}
}

// npm 装的 CLI 用 `#!/usr/bin/env node` 启动：node 和 CLI 在同一个不在 PATH 里的目录，也要能跑。
func TestRunCLIFindsInterpreterNextToCLI(t *testing.T) {
	home := fakeHome(t)
	bin := filepath.Join(home, ".nvm/versions/node/v22.23.5/bin")
	writeExe(t, filepath.Join(bin, "node"), "#!/bin/sh\ncat >/dev/null\necho pi-ok\n")
	writeExe(t, filepath.Join(bin, "pi"), "#!/usr/bin/env node\n")
	out, err := runCLI(context.Background(), "pi", t.TempDir(), "hello", CLIOptions{})
	if err != nil || strings.TrimSpace(out) != "pi-ok" {
		t.Fatalf("runCLI = %q, %v", out, err)
	}
}
