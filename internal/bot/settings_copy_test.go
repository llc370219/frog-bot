package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"frogbot/internal/onefrog"
)

func TestChannelsAreSharedButModelChoicesArePerBot(t *testing.T) {
	dir := t.TempDir()
	bs, err := OpenBots(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bs.Close)
	h := bs.Handler(nil)
	request := func(method, path, body string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.String()
	}
	key := "sk-shared-secret"
	pool, _ := json.Marshal(map[string]any{"providers": []APIChannel{
		{ID: "a", Name: "A", BaseURL: "https://a.example/v1", Enabled: true, AuthRequired: true, APIKey: &key, Models: []string{"chat-a", "agent-a"}},
		{ID: "b", Name: "B", BaseURL: "https://b.example/v1", Enabled: true, Models: []string{"big-b"}},
	}})
	if got := request("POST", "/api/channels", string(pool), http.StatusOK); strings.Contains(got, key) || !strings.Contains(got, `"has_api_key":true`) {
		t.Fatalf("channel save: %s", got)
	}
	var child BotProfile
	_ = json.Unmarshal([]byte(request("POST", "/api/bots", `{"name":"Child"}`, http.StatusOK)), &child)
	base := "/api/bots/" + child.ID

	// Every Bot sees the same channels; each one picks its own models.
	if got := request("GET", base+"/settings", "", http.StatusOK); !strings.Contains(got, "big-b") || strings.Contains(got, `"model":"chat-a"`) {
		t.Fatalf("child should see shared channels without a model choice: %s", got)
	}
	request("POST", "/api/bots/main/settings/routes", `{"default_provider_id":"a","model":"chat-a","agent_provider_id":"a","agent_model":"agent-a"}`, http.StatusOK)
	request("POST", base+"/settings/routes", `{"default_provider_id":"b","model":"big-b"}`, http.StatusOK)
	request("POST", base+"/settings/routes", `{"default_provider_id":"b","model":"missing"}`, http.StatusBadRequest)
	c := bs.runtimes[child.ID]
	if _, m := c.resolveModel(true); m != "big-b" {
		t.Fatalf("child chat: %q", m)
	}
	if _, m := c.resolveModel(false); m != "big-b" {
		t.Fatalf("empty Agent choice should follow chat: %q", m)
	}
	if _, m := bs.main.resolveModel(false); m != "agent-a" {
		t.Fatalf("main agent: %q", m)
	}
	var view channelsView
	_ = json.Unmarshal([]byte(request("GET", "/api/channels", "", http.StatusOK)), &view)
	if len(view.Usage) != 2 || view.Usage[1].ChatProviderID != "b" || view.Usage[0].AgentProviderID != "a" {
		t.Fatalf("usage: %+v", view.Usage)
	}
	// Editing a shared channel takes effect for every Bot that uses it; disabling it stops them.
	disabled := strings.Replace(string(pool), `"id":"b","name":"B","base_url":"https://b.example/v1","enabled":true`, `"id":"b","name":"B","base_url":"https://b.example/v1","enabled":false`, 1)
	request("POST", "/api/channels", disabled, http.StatusOK)
	if p, _ := c.resolveModel(true); p != nil {
		t.Fatal("disabled channel still used")
	}
	if c, ok := bs.main.pool.find("a"); !ok || c.key != key {
		t.Fatal("omitted key was not kept")
	}
	// Copy duplicates only the choice; afterwards the two Bots stay independent.
	request("POST", base+"/settings/copy", `{"from":"`+child.ID+`"}`, http.StatusBadRequest)
	request("POST", base+"/settings/copy", `{"from":"bot_0000000000000000"}`, http.StatusNotFound)
	request("POST", base+"/settings/copy", `{"from":"main"}`, http.StatusOK)
	if _, m := c.resolveModel(false); m != "agent-a" {
		t.Fatalf("copied agent: %q", m)
	}
	request("POST", base+"/settings/routes", `{"default_provider_id":"a","model":"agent-a"}`, http.StatusOK)
	if _, m := bs.main.resolveModel(true); m != "chat-a" {
		t.Fatalf("source Bot changed: %q", m)
	}
	if raw, _ := os.ReadFile(c.channelPath()); strings.Contains(string(raw), "providers") || strings.Contains(string(raw), key) {
		t.Fatalf("Bot file should only hold its choice: %s", raw)
	}
}

func TestPerBotChannelsFromOlderVersionMergeIntoSharedPool(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")}
	bs, err := OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mainKey, childKey := "sk-main", "sk-child"
	if _, err := bs.main.pool.save([]APIChannel{{ID: "same", Name: "Main", BaseURL: "https://main.example/v1", Enabled: true, AuthRequired: true, APIKey: &mainKey, Models: []string{"m"}}}); err != nil {
		t.Fatal(err)
	}
	p, err := bs.setProfile("", BotProfile{Name: "Old child"})
	if err != nil {
		t.Fatal(err)
	}
	child := bs.runtimes[p.ID]
	sealed, _ := child.vault.SealAPIKey(childKey)
	old := channelFile{Providers: []storedChannel{
		{APIChannel: APIChannel{ID: "same", Name: "Child", BaseURL: "https://child.example/v1", Enabled: true, AuthRequired: true, Models: []string{"c"}}, Sealed: sealed},
	}, DefaultID: "same", Model: "c"}
	raw, _ := json.Marshal(old)
	childDir := child.cfg.DataDir
	bs.Close()
	if err := os.WriteFile(filepath.Join(childDir, "channels.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	bs, err = OpenBots(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	child = bs.runtimes[p.ID]
	channels := bs.main.pool.public()
	if len(channels) != 2 {
		t.Fatalf("channels not merged: %+v", channels)
	}
	prov, m := child.resolveModel(true)
	if prov == nil || m != "c" || child.defaultChannel == "same" {
		t.Fatalf("child choice not remapped: %q %q", child.defaultChannel, m)
	}
	if c, _ := bs.main.pool.find(child.defaultChannel); c.key != childKey || c.BaseURL != "https://child.example/v1" {
		t.Fatalf("child key not re-sealed into the pool: %+v", c.APIChannel)
	}
	if _, m := bs.main.resolveModel(true); m != "" {
		t.Fatalf("main had no choice and should stay unset: %q", m)
	}
}

func TestAgentConnectionKeepsModelAndAuthPerBot(t *testing.T) {
	dir, bin := t.TempDir(), t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), image, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir()) // CLI 也会在家目录里找，测试不能看到本机真实装的 CLI
	t.Setenv("NVM_DIR", "")
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws")}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.saveAgentConnection(agentConnection{CLI: "claude", CLIOptions: onefrog.CLIOptions{Model: "--yolo"}}); err == nil {
		t.Fatal("accepted a flag as model")
	}
	if err := b.saveAgentConnection(agentConnection{CLI: "claude", CLIOptions: onefrog.CLIOptions{Model: "opus", Auth: onefrog.CLIAuthEnv}}); err != nil {
		t.Fatal(err)
	}
	b.Close()
	b, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if o := b.cliOptions("claude"); o.Model != "opus" || o.Auth != onefrog.CLIAuthEnv {
		t.Fatalf("options lost on restart: %+v", o)
	}
	if o := b.cliOptions("codex"); o.Model != "" || o.Auth != onefrog.CLIAuthSubscription {
		t.Fatalf("other CLIs must use defaults: %+v", o)
	}
	if err := b.saveAgentConnection(agentConnection{CLIOptions: onefrog.CLIOptions{Auth: "bogus"}}); err == nil {
		t.Fatal("accepted invalid auth")
	}
}

func TestEnvAgentModelIsSeparate(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: dir, Workspace: filepath.Join(dir, "ws"),
		InitialBaseURL: "https://env.example/v1", ChatModel: "env-chat", AgentModel: "env-agent"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, m := b.resolveModel(true); m != "env-chat" {
		t.Fatalf("chat: %q", m)
	}
	if _, m := b.resolveModel(false); m != "env-agent" {
		t.Fatalf("agent: %q", m)
	}
	if s := b.GetSettings(); s.EnvModel != "env-chat" || s.EnvAgentModel != "env-agent" {
		t.Fatalf("settings: %+v", s)
	}
}
