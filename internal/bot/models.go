package bot


import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frogbot/internal/onefrog"
)

// ProviderSettings 表示一个 API 渠道配置。
type ProviderSettings struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url"`
	APIKey       string   `json:"api_key,omitempty"` // 仅用于前端保存时传输
	HasAPIKey    bool     `json:"has_api_key"`        // 返回前端时脱敏，不回传明文
	AuthRequired bool     `json:"auth_required"`
	Enabled      bool     `json:"enabled"`
	Models       []string `json:"models"`
	HiddenModels []string `json:"hidden_models"`
}

// ModelRoute 记录某个角色使用的渠道与模型。
type ModelRoute struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

type settingsFileFormat struct {
	Providers  []ProviderSettings `json:"providers"`
	ChatRoute  ModelRoute         `json:"chat_route"`
	AgentRoute ModelRoute         `json:"agent_route"`
}

// SettingsPayload 是前端 GET/POST /api/settings 的结构体。
type SettingsPayload struct {
	Providers  []ProviderSettings `json:"providers"`
	ChatRoute  ModelRoute         `json:"chat_route"`
	AgentRoute ModelRoute         `json:"agent_route"`
}

var customClient *http.Client

func (b *Bot) settingsPath() string {
	return filepath.Join(b.cfg.DataDir, "settings.json")
}

// loadSettings 从 data/settings.json 读取配置，如果不存在则使用启动环境初始化。
func (b *Bot) loadSettings() {
	b.modelMu.Lock()
	defer b.modelMu.Unlock()

	p := b.settingsPath()
	raw, err := os.ReadFile(p)
	if err == nil {
		var f settingsFileFormat
		if err := json.Unmarshal(raw, &f); err == nil && len(f.Providers) > 0 {
			b.providers = f.Providers
			b.chatRoute = f.ChatRoute
			b.agentRoute = f.AgentRoute
			b.applyRoutesLocked()
			return
		}
	}

	// 初始默认配置：从环境变量与启动参数继承一个默认渠道
	initialPID := "primary"
	hasKey := b.initialKey != ""
	models := []string{}
	if b.cfg.AgentModel != "" {
		models = append(models, b.cfg.AgentModel)
	}
	if b.cfg.ChatModel != "" && b.cfg.ChatModel != b.cfg.AgentModel {
		models = append(models, b.cfg.ChatModel)
	}

	p0 := ProviderSettings{
		ID:           initialPID,
		Name:         "默认 API",
		BaseURL:      b.initialBaseURL,
		APIKey:       b.initialKey,
		HasAPIKey:    hasKey,
		AuthRequired: hasKey,
		Enabled:      true,
		Models:       models,
		HiddenModels: []string{},
	}

	b.providers = []ProviderSettings{p0}
	b.chatRoute = ModelRoute{ProviderID: initialPID, Model: b.cfg.ChatModel}
	b.agentRoute = ModelRoute{ProviderID: initialPID, Model: b.cfg.AgentModel}
	_ = b.saveSettingsLocked()
}

// saveSettingsLocked 持久化到 settings.json（调用方需持锁）。
func (b *Bot) saveSettingsLocked() error {
	p := b.settingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f := settingsFileFormat{
		Providers:  b.providers,
		ChatRoute:  b.chatRoute,
		AgentRoute: b.agentRoute,
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, raw, 0o600)
}

// applyRoutesLocked 根据当前的 routes 动态更新 chatProvider/agentProvider 和模型。
func (b *Bot) applyRoutesLocked() {
	findProvider := func(pid string) *ProviderSettings {
		for i := range b.providers {
			if b.providers[i].ID == pid {
				return &b.providers[i]
			}
		}
		return nil
	}

	// 找到有效的 provider
	chatP := findProvider(b.chatRoute.ProviderID)
	agentP := findProvider(b.agentRoute.ProviderID)
	if chatP == nil && len(b.providers) > 0 {
		chatP = &b.providers[0]
	}
	if agentP == nil && len(b.providers) > 0 {
		agentP = &b.providers[0]
	}

	if agentP != nil {
		key := agentP.APIKey
		if key == "" && agentP.ID == "primary" {
			key = b.initialKey
		}
		provider := onefrog.NewOpenAI(agentP.BaseURL, key)
		model := b.agentRoute.Model
		if model == "" && len(agentP.Models) > 0 {
			model = agentP.Models[0]
		}
		if model == "" {
			model = b.cfg.AgentModel
		}

		b.cfg.AgentProvider = provider
		b.cfg.AgentModel = model
		if b.mgr != nil && b.mgr.Agent != nil {
			b.mgr.Agent.Provider = provider
			b.mgr.Agent.Model = model
		}
	}

	if chatP != nil {
		key := chatP.APIKey
		if key == "" && chatP.ID == "primary" {
			key = b.initialKey
		}
		provider := onefrog.NewOpenAI(chatP.BaseURL, key)
		model := b.chatRoute.Model
		if model == "" && len(chatP.Models) > 0 {
			model = chatP.Models[0]
		}
		if model == "" {
			model = b.cfg.ChatModel
		}

		b.cfg.ChatProvider = provider
		b.cfg.ChatModel = model
	}
}

// GetSettings 返回给前端的脱敏设置数据。
func (b *Bot) GetSettings() SettingsPayload {
	b.modelMu.RLock()
	defer b.modelMu.RUnlock()

	outProviders := make([]ProviderSettings, len(b.providers))
	for i, p := range b.providers {
		out := p
		out.HasAPIKey = p.APIKey != "" || (p.ID == "primary" && b.initialKey != "")
		out.APIKey = "" // 永不回显明文 Key
		outProviders[i] = out
	}

	return SettingsPayload{
		Providers:  outProviders,
		ChatRoute:  b.chatRoute,
		AgentRoute: b.agentRoute,
	}
}

// SaveSettings 更新前端提交的设置（包括新 API 渠道、更新的 Key、模型与路由绑定）。
func (b *Bot) SaveSettings(payload SettingsPayload) error {
	b.modelMu.Lock()
	defer b.modelMu.Unlock()

	// 合并 Providers，保留未更改的 APIKey
	oldByKey := make(map[string]string)
	for _, old := range b.providers {
		oldByKey[old.ID] = old.APIKey
	}

	for i := range payload.Providers {
		p := &payload.Providers[i]
		if p.APIKey == "" {
			p.APIKey = oldByKey[p.ID]
			if p.APIKey == "" && p.ID == "primary" {
				p.APIKey = b.initialKey
			}
		}
		p.HasAPIKey = p.APIKey != ""
	}

	b.providers = payload.Providers
	b.chatRoute = payload.ChatRoute
	b.agentRoute = payload.AgentRoute

	b.applyRoutesLocked()
	return b.saveSettingsLocked()
}

// FetchRemoteModels 从指定的 Provider 远程拉取所有可用模型列表。
func (b *Bot) FetchRemoteModels(ctx context.Context, p ProviderSettings) ([]string, error) {
	key := p.APIKey
	if key == "" {
		b.modelMu.RLock()
		for _, cur := range b.providers {
			if cur.ID == p.ID {
				key = cur.APIKey
				break
			}
		}
		b.modelMu.RUnlock()
	}
	if key == "" && p.ID == "primary" {
		key = b.initialKey
	}

	baseURL := strings.TrimRight(p.BaseURL, "/")
	reqURL := baseURL + "/models"

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	if customClient != nil {
		client = customClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接模型目录，请检查地址和网络: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("模型目录返回状态码 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取模型列表失败: %w", err)
	}

	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, errors.New("模型列表格式不兼容（不是标准的 OpenAI /models 响应格式）")
	}

	var ids []string
	seen := make(map[string]bool)
	for _, m := range data.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	sort.Strings(ids)
	return ids, nil
}

