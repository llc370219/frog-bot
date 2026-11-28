package clipool

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frogbot/internal/vault"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCLIAuthManager_StatusDeviceCodeCallbackAndCredentials(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	v, err := vault.Open(dir, key)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	home := filepath.Join(dir, "home")
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0o700)

	mgr := NewCLIAuthManager(v)
	mgr.HomeDir = home
	mgr.ResolveBinaryFn = func(id string) (string, error) {
		return "/usr/local/bin/" + id, nil
	}

	// 1. Initially all 5 official CLIs (agy, codex, claude, gemini, grok) should report installed & logged out
	statuses := mgr.ListStatuses()
	if len(statuses) != 5 {
		t.Fatalf("expected 5 official CLIs, got %d", len(statuses))
	}
	if statuses[0].ID != "agy" || statuses[1].ID != "codex" {
		t.Fatalf("unexpected CLI order: [0]=%s [1]=%s", statuses[0].ID, statuses[1].ID)
	}
	for _, st := range statuses {
		if !st.Installed {
			t.Fatalf("expected %s installed", st.ID)
		}
		if st.LoggedIn {
			t.Fatalf("expected %s initially logged out, got %+v", st.ID, st)
		}
	}

	// 2. Native ~/.gemini/antigravity-cli & ~/.codex/auth.json detection
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "antigravity-cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "antigravity-cli", "google_accounts.json"), []byte(`{"active":"agy@example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"tokens":{"access_token":"tok123"},"email":"dev@example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	statuses = mgr.ListStatuses()
	if !statuses[0].LoggedIn || statuses[0].AuthMethod != "oauth" || statuses[0].AccountHint != "agy@example.com" {
		t.Fatalf("unexpected agy native auth status: %+v", statuses[0])
	}
	if !statuses[1].LoggedIn || statuses[1].AuthMethod != "oauth" || statuses[1].AccountHint != "dev@example.com" {
		t.Fatalf("unexpected codex native auth status: %+v", statuses[1])
	}
	if _, err := mgr.SaveCredential("agy", "api_key", "not-supported"); err == nil {
		t.Fatalf("expected SaveCredential for agy api_key to be rejected")
	}

	// 3. Parse ANSI-colored device code + URL
	u, code := parseAuthOutputLine("\x1b[32mOpen https://auth.openai.com/codex/device and enter code ABCD-9876\x1b[0m")
	if u != "https://auth.openai.com/codex/device" || code != "ABCD-9876" {
		t.Fatalf("parseAuthOutputLine failed: url=%q code=%q", u, code)
	}

	// 4. SSRF guard on localhost callback URL normalization
	if _, isURL, err := normalizeLoopbackCallbackURL("https://evil.example.com/steal"); !isURL || err == nil {
		t.Fatalf("expected non-http/external URL to be rejected, isURL=%v err=%v", isURL, err)
	}
	if _, isURL, err := normalizeLoopbackCallbackURL("http://169.254.169.254:80/latest"); !isURL || err == nil {
		t.Fatalf("expected metadata IP to be rejected, isURL=%v err=%v", isURL, err)
	}
	norm, isURL, err := normalizeLoopbackCallbackURL("http://localhost:1455/auth/callback?code=xyz&state=abc")
	if err != nil || !isURL || norm != "http://127.0.0.1:1455/auth/callback?code=xyz&state=abc" {
		t.Fatalf("unexpected normalized loopback URL: %q isURL=%v err=%v", norm, isURL, err)
	}

	// 5. Simulated headless OAuth session + loopback callback replay + code stdin submission
	var replayedURL string
	mgr.CallbackHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			replayedURL = r.URL.String()
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("OK")),
				Header:     make(http.Header),
			}, nil
		}),
	}
	mgr.ExecCommandFn = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// Echo an OAuth URL, read one line from stdin, then exit 0
		return exec.CommandContext(ctx, "/bin/sh", "-c", `echo "Visit https://accounts.google.com/o/oauth2/auth?client_id=test"; read line; echo "GOT:$line"`)
	}

	sess, err := mgr.StartLogin(context.Background(), "gemini", "oauth")
	if err != nil {
		t.Fatalf("StartLogin gemini: %v", err)
	}
	if sess.AuthURL != "https://accounts.google.com/o/oauth2/auth?client_id=test" {
		t.Fatalf("expected captured auth URL, got %+v", sess)
	}

	// Replay loopback callback URL
	if _, err := mgr.SubmitInput(context.Background(), sess.ID, "http://localhost:8085/oauth2callback?code=4/abc"); err != nil {
		t.Fatalf("SubmitInput callback replay: %v", err)
	}
	if replayedURL != "http://127.0.0.1:8085/oauth2callback?code=4/abc" {
		t.Fatalf("expected loopback replay, got %q", replayedURL)
	}

	// Also test raw auth code submission into stdin -> process exits 0 -> succeeded
	after, err := mgr.SubmitInput(context.Background(), sess.ID, "AUTH-CODE-XYZ")
	if err != nil {
		t.Fatalf("SubmitInput raw code: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for after.Status != "succeeded" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if cur, ok := mgr.GetSession(sess.ID); ok {
			after = cur
		}
	}
	if after.Status != "succeeded" {
		t.Fatalf("expected succeeded after stdin submit, got %+v", after)
	}

	// 6. Save encrypted Claude OAuth setup-token + Grok API key, verify EnvOverrides and Logout
	stClaude, err := mgr.SaveCredential("claude", "oauth_token", "sk-ant-oat-secret-12345678")
	if err != nil || !stClaude.LoggedIn || stClaude.AuthMethod != "oauth_token" {
		t.Fatalf("SaveCredential claude failed: %+v err=%v", stClaude, err)
	}
	stGrok, err := mgr.SaveCredential("grok", "api_key", "xai-test-key-99999")
	if err != nil || !stGrok.LoggedIn || stGrok.AuthMethod != "api_key" {
		t.Fatalf("SaveCredential grok failed: %+v err=%v", stGrok, err)
	}
	envs := mgr.EnvOverrides()
	if envs["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat-secret-12345678" || envs["XAI_API_KEY"] != "xai-test-key-99999" {
		t.Fatalf("unexpected EnvOverrides: %+v", envs)
	}

	stAfterOut, err := mgr.Logout("grok", true)
	if err != nil || stAfterOut.LoggedIn {
		t.Fatalf("expected grok logged out, got %+v err=%v", stAfterOut, err)
	}
}
