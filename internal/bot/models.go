package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"frogbot/internal/onefrog"
)

// APISettings describes the connection shared by chat and background tasks.
// APIKey is write-only: omitted keeps the key for the same URL; empty clears it.
type APISettings struct {
	BaseURL           string       `json:"base_url"`
	Model             string       `json:"model"`
	APIKey            *string      `json:"api_key,omitempty"`
	HasAPIKey         bool         `json:"has_api_key"`
	Providers         []APIChannel `json:"providers,omitempty"`
	DefaultProviderID string       `json:"default_provider_id,omitempty"`
	ProviderID        string       `json:"provider_id,omitempty"`
}

type settingsFile struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	Sealed  string `json:"sealed_api_key,omitempty"`
}

type legacyProvider struct {
	ID      string   `json:"id"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`
}

type legacyRoute struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

type legacySettings struct {
	Providers  []legacyProvider `json:"providers"`
	ChatRoute  legacyRoute      `json:"chat_route"`
	AgentRoute legacyRoute      `json:"agent_route"`
}

type modelConnection struct {
	baseURL, model, key string
	provider            onefrog.Provider
}

func (b *Bot) settingsPath() string {
	return filepath.Join(b.cfg.DataDir, "settings.json")
}

func (b *Bot) loadSettings() error {
	if b.cfg.InitialBaseURL != "" {
		model := b.cfg.ChatModel
		if model == "" {
			model = b.cfg.AgentModel
		}
		b.model = newConnection(b.cfg.InitialBaseURL, model, b.cfg.InitialKey)
	}

	raw, err := os.ReadFile(b.settingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved settingsFile
	if err := json.Unmarshal(raw, &saved); err != nil {
		return err
	}
	if saved.BaseURL != "" {
		var key string
		if saved.Sealed != "" {
			key, err = b.vault.OpenAPIKey(saved.Sealed)
			if err != nil {
				return err
			}
		}
		b.model = newConnection(saved.BaseURL, saved.Model, key)
		return nil
	}
	// Start with the channel actually used for chat in older settings.
	var old legacySettings
	if err := json.Unmarshal(raw, &old); err != nil {
		return err
	}
	idx := slices.IndexFunc(old.Providers, func(p legacyProvider) bool { return p.ID == old.ChatRoute.ProviderID && p.BaseURL != "" })
	if idx < 0 {
		idx = slices.IndexFunc(old.Providers, func(p legacyProvider) bool { return p.BaseURL != "" })
	}
	if idx >= 0 {
		p := old.Providers[idx]
		model := ""
		if p.ID == old.ChatRoute.ProviderID {
			model = old.ChatRoute.Model
		}
		if model == "" && p.ID == old.AgentRoute.ProviderID {
			model = old.AgentRoute.Model
		}
		if model == "" && len(p.Models) > 0 {
			model = p.Models[0]
		}
		key := p.APIKey
		if key == "" && p.ID == "primary" {
			if normalized, err := normalizeBaseURL(p.BaseURL); err == nil && normalized == b.model.baseURL {
				key = b.model.key
			}
		}
		b.model = newConnection(p.BaseURL, model, key)
	}
	return nil
}

func newConnection(baseURL, model, key string) modelConnection {
	if normalized, err := normalizeBaseURL(baseURL); err == nil {
		baseURL = normalized
	}
	p := onefrog.NewOpenAI(baseURL, key)
	p.Client.CheckRedirect = noRedirect
	return modelConnection{baseURL: baseURL, model: model, key: key, provider: p}
}

func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("请输入有效的 HTTP 或 HTTPS 接口地址，地址中不要包含密钥、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	for _, endpoint := range []string{"/chat/completions", "/models"} {
		if strings.HasSuffix(u.Path, endpoint) {
			u.Path = strings.TrimSuffix(u.Path, endpoint)
			break
		}
	}
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (b *Bot) resolveModel(chat bool) (onefrog.Provider, string) {
	if b.modelOwner != nil {
		return b.modelOwner.resolveModel(chat)
	}
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()
	if b.model.provider != nil {
		if b.model.model == "" {
			return nil, ""
		}
		return b.model.provider, b.model.model
	}
	if chat {
		return b.cfg.ChatProvider, b.cfg.ChatModel
	}
	return b.cfg.AgentProvider, b.cfg.AgentModel
}

type settingsReviewer struct {
	bot   *Bot
	model string
}

func (r settingsReviewer) Review(ctx context.Context, req onefrog.ReviewRequest) (onefrog.Verdict, error) {
	provider, _ := r.bot.resolveModel(false)
	if provider == nil {
		return onefrog.Verdict{}, errors.New("尚未配置模型 API")
	}
	reviewer := onefrog.LLMReviewer{Provider: provider, Model: r.model}
	return reviewer.Review(ctx, req)
}

func (b *Bot) GetSettings() APISettings {
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()
	s := b.model.public()
	s.Providers = b.publicChannels()
	s.DefaultProviderID = b.defaultChannel
	if s.DefaultProviderID == "" && len(s.Providers) > 0 {
		s.DefaultProviderID = s.Providers[0].ID
	}
	return s
}

func (m modelConnection) public() APISettings {
	return APISettings{BaseURL: m.baseURL, Model: m.model, HasAPIKey: m.key != ""}
}

func (m modelConnection) keyFor(draft APISettings, baseURL string) string {
	if draft.APIKey != nil {
		return strings.TrimSpace(*draft.APIKey)
	}
	if baseURL == m.baseURL {
		return m.key
	}
	return ""
}

func (b *Bot) SaveSettings(draft APISettings) (APISettings, error) {
	if draft.Providers != nil {
		return b.saveChannels(draft)
	}
	baseURL, err := normalizeBaseURL(draft.BaseURL)
	if err != nil {
		return APISettings{}, err
	}
	model := strings.TrimSpace(draft.Model)
	if model == "" {
		return APISettings{}, errors.New("请输入模型 ID")
	}
	b.modelMu.Lock()
	defer b.modelMu.Unlock()
	key := b.catalogKey(draft, baseURL)
	if len(b.channels) == 0 {
		if _, statErr := os.Stat(b.channelPath()); statErr == nil {
			return b.saveChannelsLocked(APISettings{DefaultProviderID: "primary", Model: model, Providers: []APIChannel{{
				ID: "primary", Name: "主力 API", BaseURL: baseURL, Enabled: true, AuthRequired: key != "", APIKey: &key, Models: []string{model},
			}}})
		}
	}
	if len(b.channels) > 0 {
		providers := b.publicChannels()
		for i := range providers {
			if providers[i].ID == b.defaultChannel {
				p := &providers[i]
				p.BaseURL, p.Enabled, p.AuthRequired, p.APIKey = baseURL, true, key != "", &key
				p.Models = append(slices.Clone(p.Models), model)
				p.HiddenModels = slices.DeleteFunc(slices.Clone(p.HiddenModels), func(id string) bool { return id == model })
				return b.saveChannelsLocked(APISettings{Providers: providers, DefaultProviderID: b.defaultChannel, Model: model})
			}
		}
	}
	f := settingsFile{BaseURL: baseURL, Model: model}
	if key != "" {
		f.Sealed, err = b.vault.SealAPIKey(key)
		if err != nil {
			return APISettings{}, err
		}
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return APISettings{}, err
	}
	tmp, err := os.CreateTemp(b.cfg.DataDir, "settings-*.tmp")
	if err != nil {
		return APISettings{}, fmt.Errorf("保存设置失败: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(raw)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return APISettings{}, fmt.Errorf("保存设置失败: %w", err)
	}
	if err := os.Rename(tmp.Name(), b.settingsPath()); err != nil {
		return APISettings{}, fmt.Errorf("保存设置失败: %w", err)
	}
	b.model = newConnection(baseURL, model, key)
	return b.model.public(), nil
}

var modelCatalogClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect}

func (b *Bot) FetchRemoteModels(ctx context.Context, draft APISettings) ([]string, error) {
	baseURL, err := normalizeBaseURL(draft.BaseURL)
	if err != nil {
		return nil, err
	}
	b.modelMu.RLock()
	key := b.catalogKey(draft, baseURL)
	b.modelMu.RUnlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, errors.New("无法创建模型列表请求")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := modelCatalogClient.Do(req)
	if err != nil {
		return nil, errors.New("无法连接模型服务，请检查接口地址和网络")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("模型服务拒绝访问（HTTP %d），请检查 API Key", resp.StatusCode)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return nil, errors.New("此地址未提供模型列表，请检查接口地址或手动填写模型 ID")
	default:
		return nil, fmt.Errorf("读取模型列表失败（HTTP %d）", resp.StatusCode)
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data); err != nil || data.Data == nil {
		return nil, errors.New("模型列表格式不兼容，请手动填写模型 ID")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range data.Data {
		if id := strings.TrimSpace(m.ID); id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	slices.Sort(ids)
	return ids, nil
}
