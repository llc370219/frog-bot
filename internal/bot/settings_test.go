package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"frogbot/internal/onefrog"
)

const testKey = "sk-test-SUPERSECRET-0123456789abcdef"

// fakeOpenAI 是一个最小的 OpenAI 兼容服务：检查 Authorization，回一段 SSE。
func fakeOpenAI(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"bad key `+r.Header.Get("Authorization")+`"}}`)
			return
		}
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &models
}

func newSettingsBot(t *testing.T, env ModelEnv) (*Bot, string) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"), ModelEnv: env, LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	return b, dir
}

func do(t *testing.T, h http.Handler, method, path string, body any, hdr map[string]string) (int, string) {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Host = "127.0.0.1:8080"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func TestSettingsFirstRunSaveAndUse(t *testing.T) {
	ai, models := fakeOpenAI(t)
	b, dir := newSettingsBot(t, ModelEnv{})
	h := b.HandlerFS(nil)

	code, out := do(t, h, "GET", "/api/settings", nil, nil)
	if code != 200 || !strings.Contains(out, `"configured":false`) {
		t.Fatalf("first run should be unconfigured: %d %s", code, out)
	}
	if _, err := b.Send("你好"); err == nil {
		t.Fatal("Send should fail before a model is configured")
	}

	// 错误的 Key：测试连接失败，且错误信息里没有 Key
	code, out = do(t, h, "POST", "/api/settings/test", map[string]string{"base_url": ai.URL, "api_key": "sk-wrong-key-zzzzzzzz", "agent_model": "m1"}, nil)
	if code != 400 || strings.Contains(out, "sk-wrong-key-zzzzzzzz") {
		t.Fatalf("bad key test should fail without echoing key: %d %s", code, out)
	}
	code, out = do(t, h, "POST", "/api/settings/test", map[string]string{"base_url": ai.URL, "api_key": testKey, "agent_model": "m1"}, nil)
	if code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("test connection: %d %s", code, out)
	}
	code, out = do(t, h, "POST", "/api/settings", map[string]string{"base_url": ai.URL + "/", "api_key": testKey, "agent_model": "m1", "chat_model": "c1"}, nil)
	if code != 200 || !strings.Contains(out, `"configured":true`) || !strings.Contains(out, `"has_api_key":true`) {
		t.Fatalf("save: %d %s", code, out)
	}
	if strings.Contains(out, testKey) {
		t.Fatal("save response echoed the key")
	}
	_, out = do(t, h, "GET", "/api/settings", nil, nil)
	if strings.Contains(out, testKey) || !strings.Contains(out, `"chat_model":"c1"`) {
		t.Fatalf("settings view: %s", out)
	}
	// Key 只以密文落盘
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if data, _ := os.ReadFile(p); bytes.Contains(data, []byte(testKey)) {
				t.Errorf("plaintext key found in %s", p)
			}
		}
		return nil
	})
	// 保存后立即生效：Provider 用新的地址、Key 和模型
	msg, err := switchProvider{m: b.models, chat: true}.Chat(context.Background(), chatReq(), nil)
	if err != nil || msg.Content != "pong" {
		t.Fatalf("chat via saved settings: %v %q", err, msg.Content)
	}
	if got := (*models)[len(*models)-1]; got != "c1" {
		t.Fatalf("chat model = %s", got)
	}
	// 留空 Key = 不修改
	code, _ = do(t, h, "POST", "/api/settings", map[string]string{"base_url": ai.URL, "agent_model": "m2"}, nil)
	if code != 200 {
		t.Fatal("update without key failed")
	}
	if _, err := (switchProvider{m: b.models}).Chat(context.Background(), chatReq(), nil); err != nil {
		t.Fatalf("key should be kept: %v", err)
	}
	if got := (*models)[len(*models)-1]; got != "m2" {
		t.Fatalf("agent model = %s", got)
	}

	// 重启后还在
	b.Close()
	b2, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if !b2.Configured() {
		t.Fatal("settings lost after restart")
	}
	if _, err := (switchProvider{m: b2.models}).Chat(context.Background(), chatReq(), nil); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

func TestSettingsEnvOverride(t *testing.T) {
	ai, models := fakeOpenAI(t)
	b, _ := newSettingsBot(t, ModelEnv{BaseURL: ai.URL, APIKey: testKey, AgentModel: "env-agent"})
	defer b.Close()
	h := b.HandlerFS(nil)
	_, out := do(t, h, "GET", "/api/settings", nil, nil)
	if !strings.Contains(out, `"configured":true`) || !strings.Contains(out, `"api_key":true`) || strings.Contains(out, testKey) {
		t.Fatalf("env view: %s", out)
	}
	// 网页里改不了环境变量提供的字段
	code, _ := do(t, h, "POST", "/api/settings", map[string]string{"base_url": "http://example.invalid", "agent_model": "web"}, nil)
	if code != 200 {
		t.Fatal("save failed")
	}
	if _, err := (switchProvider{m: b.models}).Chat(context.Background(), chatReq(), nil); err != nil {
		t.Fatal(err)
	}
	if got := (*models)[len(*models)-1]; got != "env-agent" {
		t.Fatalf("env should win, got %s", got)
	}
	// 事件脱敏也覆盖环境变量里的 Key
	if bytes.Contains(b.store.Scrub([]byte("x "+testKey)), []byte(testKey)) {
		t.Fatal("env key not redacted")
	}
}

func TestLocalOnly(t *testing.T) {
	b, _ := newSettingsBot(t, ModelEnv{})
	defer b.Close()
	h := b.HandlerFS(nil)
	r := httptest.NewRequest("GET", "/api/health", nil)
	r.Host = "evil.example:8080" // DNS 重绑定
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("foreign host should be rejected, got %d", w.Code)
	}
	if code, _ := do(t, h, "GET", "/api/health", nil, nil); code != 200 {
		t.Fatalf("health: %d", code)
	}
	if code, _ := do(t, h, "POST", "/api/stop", nil, map[string]string{"Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("cross-site POST should be rejected, got %d", code)
	}
	if code, _ := do(t, h, "POST", "/api/stop", nil, map[string]string{"Origin": "http://localhost:5173"}); code != 200 {
		t.Fatalf("local dev origin should pass, got %d", code)
	}
}

func chatReq() onefrog.ChatRequest {
	return onefrog.ChatRequest{Messages: []onefrog.Message{{Role: "user", Content: "ping"}}}
}
