package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frogbot/internal/onefrog"
)

func TestSettingsFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth := "Bearer test-key"
		if strings.HasPrefix(r.URL.Path, "/new/") {
			wantAuth = ""
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Error("request used the wrong credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models", "/new/models":
			fmt.Fprint(w, `{"data":[{"id":"test-model"},{"id":"test-model"}]}`)
		case "/v1/chat/completions", "/new/chat/completions":
			var body struct{ Model string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "test-model" {
				t.Errorf("unexpected model: %+v (%v)", body, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"model replied\"}}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	post := func(b *Bot, path string, payload APISettings) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		b.Handler("").ServeHTTP(w, req)
		return w
	}

	t.Run("first-run setup and both model paths", func(t *testing.T) {
		if s := b.GetSettings(); s.BaseURL != "" || s.HasAPIKey {
			t.Fatal("expected empty settings on first run")
		}
		key := "test-key"
		draft := APISettings{BaseURL: upstream.URL + "/v1/chat/completions/", APIKey: &key}
		w := post(b, "/api/settings/models", draft)
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"models":["test-model"]}` {
			t.Fatalf("model catalog: %d %s", w.Code, w.Body)
		}
		draft.Model = " test-model "
		w = post(b, "/api/settings", draft)
		if w.Code != 200 || strings.Contains(w.Body.String(), key) || !b.GetSettings().HasAPIKey {
			t.Fatalf("save failed or exposed key: HTTP %d", w.Code)
		}
		file, err := os.ReadFile(b.settingsPath())
		if err != nil || bytes.Contains(file, []byte(key)) {
			t.Fatal("settings should be persisted without the plaintext key")
		}
		b.runTurn([]Message{{Role: "user", Text: "hello"}})
		if messages := b.store.Messages(); len(messages) == 0 || messages[len(messages)-1].Text != "model replied" {
			t.Fatal("chat did not use the saved API")
		}
		res, err := b.mgr.Agent.Run(context.Background(), onefrog.Task{Input: "work", Workspace: cfg.Workspace})
		if err != nil || res.Output != "model replied" {
			t.Fatalf("agent did not use the saved API: %+v (%v)", res, err)
		}
	})
	b.Close()

	b, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if s := b.GetSettings(); s.BaseURL != upstream.URL+"/v1" || s.Model != "test-model" || !s.HasAPIKey {
		t.Fatalf("incorrect settings after restart: %+v", s)
	}
	if w := post(b, "/api/settings/models", b.GetSettings()); w.Code != 200 {
		t.Fatalf("saved key wasn't reused for the original address: %d %s", w.Code, w.Body)
	}

	// A changed address never receives the original key, for discovery or chat.
	replacement := APISettings{BaseURL: upstream.URL + "/new", Model: "test-model"}
	if w := post(b, "/api/settings/models", replacement); w.Code != 200 {
		t.Fatalf("model catalog at the new address: %d %s", w.Code, w.Body)
	}
	if w := post(b, "/api/settings", replacement); w.Code != 200 || b.GetSettings().HasAPIKey {
		t.Fatalf("saving the new address should drop the previous key: HTTP %d", w.Code)
	}
	provider, model := b.resolveModel(true)
	if _, err := provider.Chat(context.Background(), onefrog.ChatRequest{Model: model}, nil); err != nil {
		t.Fatalf("replacement connection: %v", err)
	}
}

func TestLegacySettingsUseChatRoute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{
		"providers":[{"id":"unused","base_url":"https://old.example/v1","api_key":"unused"},
			{"id":"chat","base_url":"https://chat.example/v1","api_key":"chat-key"}],
		"chat_route":{"provider_id":"chat","model":"chat-model"},
		"agent_route":{"provider_id":"unused","model":"agent-model"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{DataDir: dir, Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := b.GetSettings()
	if s.BaseURL != "https://chat.example/v1" || s.Model != "chat-model" || !s.HasAPIKey || s.APIKey != nil {
		t.Fatalf("legacy chat connection not loaded correctly: %+v", s)
	}
}
