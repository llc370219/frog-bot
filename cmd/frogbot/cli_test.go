package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frogbot/internal/remote"
)

type handlerRoundTripper struct {
	h http.Handler
}

func (rt handlerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func TestRenderQRTerminalColorAndPlain(t *testing.T) {
	uri := "frog://e2ee/0123456789abcdef?host=test&relay=wss%3A%2F%2Frelay.632219.xyz%2Fws#HomePC"
	colored, err := remote.RenderQRTerminal(uri, true)
	if err != nil {
		t.Fatalf("RenderQRTerminal color: %v", err)
	}
	if !strings.Contains(colored, "\x1b[30;47m") || !strings.Contains(colored, "\x1b[0m") {
		t.Fatalf("expected ANSI color codes in colored QR")
	}
	if !strings.ContainsAny(colored, "█▀▄") {
		t.Fatalf("expected Unicode half-block glyphs in QR")
	}

	plain, err := remote.RenderQRTerminal(uri, false)
	if err != nil {
		t.Fatalf("RenderQRTerminal plain: %v", err)
	}
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("plain QR must not contain ANSI escape codes")
	}
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) < 10 {
		t.Fatalf("QR height too small: %d lines", len(lines))
	}
}

func TestRuntimeStateOwnershipAndControlAuth(t *testing.T) {
	dir := t.TempDir()
	st := runtimeState{
		PID:          1001,
		Version:      "v0.1.11-beta.10",
		Mode:         "local",
		Addr:         "127.0.0.1:8080",
		URL:          "http://127.0.0.1:8080",
		DataDir:      dir,
		ControlAddr:  "127.0.0.1:55555",
		ControlToken: "secret-token-123",
	}
	if err := writeRuntimeState(dir, st); err != nil {
		t.Fatal(err)
	}
	got, err := readRuntimeState(dir)
	if err != nil || got.PID != 1001 || got.ControlToken != "secret-token-123" {
		t.Fatalf("readRuntimeState mismatch: got=%+v err=%v", got, err)
	}
	// 旧进程 PID=999 不应误删新进程 PID=1001 的 runtime.json
	removeRuntimeStateIfOwned(dir, 999)
	if _, err := os.Stat(runtimeStatePath(dir)); err != nil {
		t.Fatalf("runtime.json should still exist when removed by non-owner PID")
	}
	removeRuntimeStateIfOwned(dir, 1001)
	if _, err := os.Stat(runtimeStatePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("runtime.json should be removed by owning PID")
	}

	var stopped atomic.Bool
	h := buildLocalControlHandler(nil, st, st.ControlToken, func() { stopped.Store(true) })

	// 1) 未带 Token 必须拒绝
	unauthReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/ctl/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, unauthReq)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rec.Code)
	}

	// 2) 带正确 Token 通过 controlClient 正常调用 status 与 stop
	cc := &controlClient{
		state: st,
		http:  &http.Client{Transport: handlerRoundTripper{h: h}},
	}
	live, err := cc.Status()
	if err != nil {
		t.Fatalf("cc.Status: %v", err)
	}
	if live.PID != 1001 || live.URL != "http://127.0.0.1:8080" {
		t.Fatalf("unexpected live status: %+v", live)
	}
	if err := cc.Stop(); err != nil {
		t.Fatalf("cc.Stop: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !stopped.Load() {
		t.Fatalf("expected onStop callback to fire")
	}
}

func TestCLIConfigEnvAndTailLogs(t *testing.T) {
	dir := t.TempDir()
	env := resolveCLIEnv(dir)
	cfg := loadCLIConfig(env)
	if cfg.Mode != "local" {
		t.Fatalf("default mode should be local, got %q", cfg.Mode)
	}

	if err := applyConfigKV(env, "mode", "server"); err != nil {
		t.Fatal(err)
	}
	if err := applyConfigKV(env, "port", "9090"); err != nil {
		t.Fatal(err)
	}
	cfg2 := loadCLIConfig(env)
	if cfg2.Mode != "server" || cfg2.Port != 9090 {
		t.Fatalf("unexpected saved cliConfig: %+v", cfg2)
	}
	serveArgs := buildServeArgs(env, cfg2)
	joined := strings.Join(serveArgs, " ")
	if !strings.Contains(joined, "-server") || !strings.Contains(joined, "-port 9090") || !strings.Contains(joined, "-no-browser") {
		t.Fatalf("unexpected buildServeArgs: %v", serveArgs)
	}

	// 测试 systemd env 文件读写
	envFile := filepath.Join(dir, "frogbot.env")
	if err := os.WriteFile(envFile, []byte("# comment\nFROGBOT_MODE=server\nADDR=0.0.0.0:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateEnvFileKey(envFile, "ADDR", "0.0.0.0:9090"); err != nil {
		t.Fatal(err)
	}
	kv := parseEnvFile(envFile)
	if kv["FROGBOT_MODE"] != "server" || kv["ADDR"] != "0.0.0.0:9090" {
		t.Fatalf("env file roundtrip mismatch: %+v", kv)
	}

	// 测试纯 Go 日志 tail 与 -f 实时追踪
	logPath := filepath.Join(dir, "frog.log")
	if err := os.WriteFile(logPath, []byte("line1\nline2\nline3\nline4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var tailBuf bytes.Buffer
	if err := tailLogFile(context.Background(), logPath, 2, false, &tailBuf); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(tailBuf.String()); got != "line3\nline4" {
		t.Fatalf("tail last 2 lines got %q, want line3\\nline4", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var followBuf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- tailLogFile(ctx, logPath, 1, true, &followBuf)
	}()
	time.Sleep(100 * time.Millisecond)
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("line5-live\n")
	_ = f.Close()
	time.Sleep(350 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(followBuf.String(), "line5-live") {
		t.Fatalf("follow mode missed appended line: %q", followBuf.String())
	}
}

func TestDispatchCLILegacyFlagsFallthrough(t *testing.T) {
	// 带传统 flag（如 -server / -addr）必须交给 serveMain 处理，不能被新 CLI 吞掉
	if dispatchCLI([]string{"-server"}) {
		t.Fatalf("expected -server to fall through to legacy serveMain")
	}
	if dispatchCLI([]string{"-addr", "127.0.0.1:8080", "-no-browser"}) {
		t.Fatalf("expected -addr to fall through to legacy serveMain")
	}
}
