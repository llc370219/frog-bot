package bot

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"frogbot/internal/onefrog"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSettingsFlow(t *testing.T) {
	dir := t.TempDir()

	customClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/v1/models" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"},{"id":"deepseek-chat"}]}`))),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		}),
	}
	defer func() { customClient = nil }()

	ai := &fakeAI{agent: func(_ context.Context, _ []onefrog.Message) (onefrog.Message, error) {
		return onefrog.Message{Content: "done"}, nil
	}}

	b, err := New(Config{
		Name:           "Frog",
		DataDir:        filepath.Join(dir, "data"),
		Workspace:      filepath.Join(dir, "ws"),
		ChatProvider:   ai,
		ChatModel:      "gpt-4o-mini",
		AgentProvider:  ai,
		AgentModel:     "gpt-4o",
		InitialBaseURL: "http://api.mock.test/v1",
		InitialKey:     "test-key",
		Demo:           true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	st := b.GetSettings()
	if len(st.Providers) == 0 {
		t.Fatal("expected default provider")
	}
	if st.Providers[0].APIKey != "" {
		t.Fatal("api key should be masked")
	}

	// 测试拉取模型
	models, err := b.FetchRemoteModels(context.Background(), st.Providers[0])
	if err != nil {
		t.Fatalf("fetch models failed: %v", err)
	}
	if len(models) != 3 || models[0] != "deepseek-chat" {
		t.Fatalf("unexpected models: %v", models)
	}

	// 测试保存设置并切换模型
	st.Providers[0].Models = models
	st.ChatRoute = ModelRoute{ProviderID: st.Providers[0].ID, Model: "deepseek-chat"}
	if err := b.SaveSettings(st); err != nil {
		t.Fatalf("save settings failed: %v", err)
	}

	if b.cfg.ChatModel != "deepseek-chat" {
		t.Fatalf("chat model not updated: %s", b.cfg.ChatModel)
	}
}
