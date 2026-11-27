package bot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"frogbot/internal/vault"
)

type proxyRTFunc func(*http.Request) (*http.Response, error)

func (f proxyRTFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyGateway_DisclaimerAndChannelCoexistence(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	v, err := vault.Open(dir, key)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	pool, err := openPool(dir, v)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}

	// Pre-existing custom API channel must remain untouched!
	customKey := "sk-custom-user-api"
	if _, err := pool.save([]APIChannel{
		{
			ID:           "custom-1",
			Name:         "官方自定义 API",
			BaseURL:      "https://api.example.com/v1",
			Enabled:      true,
			AuthRequired: true,
			APIKey:       &customKey,
			Models:       []string{"gpt-4.1"},
		},
	}); err != nil {
		t.Fatalf("seed custom channel: %v", err)
	}

	mgr := NewProxyGatewayManager(dir, v, pool)
	mgr.skipProbes = true
	mgr.findBinFn = func([]string) string { return "" }
	mgr.httpClient = &http.Client{
		Transport: proxyRTFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/models") {
				return &http.Response{
					StatusCode: 200,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5.3-codex"},{"id":"claude-sonnet-4-5"}]}`)),
				}, nil
			}
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
		}),
	}

	// 1. Enabling WITHOUT accepting disclaimer MUST fail
	_, err = mgr.Save(context.Background(), ProxyGatewaySaveInput{
		Enabled:            true,
		DisclaimerAccepted: false,
		Engine:             "cliproxyapi",
		BaseURL:            "http://127.0.0.1:8317/v1",
		SyncChannel:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "免责声明") {
		t.Fatalf("expected mandatory disclaimer rejection, got err=%v", err)
	}

	// 2. Enabling WITH disclaimer accepted succeeds, discovers models, and syncs dedicated channel alongside custom API
	proxyKey := "local-proxy-secret"
	st, err := mgr.Save(context.Background(), ProxyGatewaySaveInput{
		Enabled:            true,
		DisclaimerAccepted: true,
		Engine:             "cliproxyapi",
		BaseURL:            "http://127.0.0.1:8317/v1",
		ChannelName:        "账号反代 · CLIProxyAPI",
		AuthRequired:       true,
		APIKey:             &proxyKey,
		SyncChannel:        true,
	})
	if err != nil {
		t.Fatalf("Save with disclaimer failed: %v", err)
	}
	if !st.Config.Enabled || !st.Config.DisclaimerAccepted || !st.ChannelSynced || !st.ProbeOK {
		t.Fatalf("unexpected proxy status: %+v", st)
	}
	if len(st.LiveModels) != 2 {
		t.Fatalf("expected 2 discovered models, got %+v", st.LiveModels)
	}

	// Verify BOTH custom API and reverse proxy channel coexist cleanly
	channels := pool.public()
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels (custom + proxy), got %d: %+v", len(channels), channels)
	}
	if channels[0].ID != "custom-1" || !channels[0].Enabled || channels[1].ID != ProxyChannelID || !channels[1].Enabled {
		t.Fatalf("unexpected channel pool state: %+v", channels)
	}

	// 3. Disabling proxy disables ONLY proxy-gateway channel while keeping custom-1 active
	stOff, err := mgr.Save(context.Background(), ProxyGatewaySaveInput{
		Enabled:            false,
		DisclaimerAccepted: true,
		Engine:             "cliproxyapi",
		BaseURL:            "http://127.0.0.1:8317/v1",
		SyncChannel:        true,
	})
	if err != nil {
		t.Fatalf("disable proxy failed: %v", err)
	}
	if stOff.Config.Enabled || stOff.ChannelSynced {
		t.Fatalf("expected proxy disabled & unsynced, got %+v", stOff)
	}
	channels = pool.public()
	if len(channels) != 2 || !channels[0].Enabled || channels[1].Enabled {
		t.Fatalf("custom channel should remain enabled while proxy channel disabled: %+v", channels)
	}
}
