package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frogbot/internal/onefrog"
	"frogbot/internal/vault"
)

// 模型设置：网页里填的 API 地址 / 模型名存在 data/config/model.json，
// API Key 用同一把主密钥加密存在 data/config/secrets.json（独立于给任务用的密钥库，
// 所以模型和工具都引用不到它）。环境变量优先于网页设置。

const modelKeyName = "MODEL_API_KEY"

// ModelEnv 是环境变量提供的模型配置（非空字段覆盖网页设置）。
type ModelEnv struct {
	BaseURL     string
	APIKey      string
	ChatModel   string
	AgentModel  string
	ChatBaseURL string
	ChatAPIKey  string
}

// ModelSettings 是网页里保存的（不含 API Key）。
type ModelSettings struct {
	BaseURL    string `json:"base_url"`
	ChatModel  string `json:"chat_model,omitempty"`
	AgentModel string `json:"agent_model"`
}

// SettingsView 是返回给前端的设置：永远不含 API Key 明文。
type SettingsView struct {
	Configured bool            `json:"configured"`
	BaseURL    string          `json:"base_url"`
	ChatModel  string          `json:"chat_model"`
	AgentModel string          `json:"agent_model"`
	HasAPIKey  bool            `json:"has_api_key"`
	Locked     map[string]bool `json:"locked"` // 由环境变量提供、网页改不了的字段
	DataDir    string          `json:"data_dir"`
	Workspace  string          `json:"workspace"`
}

// SettingsInput 是前端提交的设置。APIKey 为空表示不修改已保存的 Key。
type SettingsInput struct {
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	ClearAPIKey bool   `json:"clear_api_key"`
	ChatModel   string `json:"chat_model"`
	AgentModel  string `json:"agent_model"`
}

type modelConf struct {
	dir   string
	env   ModelEnv
	vault *vault.Vault

	mu    sync.RWMutex
	saved ModelSettings
	chat  *onefrog.OpenAI
	agent *onefrog.OpenAI
	eff   effective
}

type effective struct {
	baseURL, apiKey, chatModel, agentModel, chatBaseURL, chatAPIKey string
}

func (e effective) ok() bool { return e.baseURL != "" && e.agentModel != "" }

func openModelConf(dataDir string, key []byte, env ModelEnv) (*modelConf, error) {
	dir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	v, err := vault.Open(dir, key)
	if err != nil {
		return nil, fmt.Errorf("打开模型设置失败: %w", err)
	}
	m := &modelConf{dir: dir, env: trimEnv(env), vault: v}
	if b, err := os.ReadFile(filepath.Join(dir, "model.json")); err == nil {
		if err := json.Unmarshal(b, &m.saved); err != nil {
			return nil, fmt.Errorf("data/config/model.json 格式不对: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	m.rebuild()
	return m, nil
}

func trimEnv(e ModelEnv) ModelEnv {
	t := strings.TrimSpace
	return ModelEnv{t(e.BaseURL), t(e.APIKey), t(e.ChatModel), t(e.AgentModel), t(e.ChatBaseURL), t(e.ChatAPIKey)}
}

func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// rebuild 根据环境变量 + 已保存设置重新算出生效配置。调用方持有写锁（或在初始化时）。
func (m *modelConf) rebuild() {
	saved := ""
	if vals, err := m.vault.Values([]string{modelKeyName}); err == nil {
		saved = vals[modelKeyName]
	}
	e := effective{
		baseURL:    strings.TrimRight(first(m.env.BaseURL, m.saved.BaseURL), "/"),
		apiKey:     first(m.env.APIKey, saved),
		agentModel: first(m.env.AgentModel, m.saved.AgentModel),
	}
	e.chatModel = first(m.env.ChatModel, m.saved.ChatModel, e.agentModel)
	e.chatBaseURL = strings.TrimRight(first(m.env.ChatBaseURL, e.baseURL), "/")
	e.chatAPIKey = first(m.env.ChatAPIKey, e.apiKey)
	m.eff = e
	m.agent = onefrog.NewOpenAI(e.baseURL, e.apiKey)
	m.chat = onefrog.NewOpenAI(e.chatBaseURL, e.chatAPIKey)
}

func (m *modelConf) current(chat bool) (*onefrog.OpenAI, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.eff.ok() {
		return nil, "", errNotConfigured
	}
	if chat {
		return m.chat, m.eff.chatModel, nil
	}
	return m.agent, m.eff.agentModel, nil
}

var errNotConfigured = errors.New("还没配置模型：请点右上角的「设置」，填好 API 地址、API Key 和模型名")

func (m *modelConf) Configured() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.eff.ok()
}

func (m *modelConf) models() (chat, agent string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.eff.chatModel, m.eff.agentModel
}

func (m *modelConf) View() SettingsView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return SettingsView{
		Configured: m.eff.ok(),
		BaseURL:    m.eff.baseURL,
		ChatModel:  m.eff.chatModel,
		AgentModel: m.eff.agentModel,
		HasAPIKey:  m.eff.apiKey != "",
		Locked: map[string]bool{"base_url": m.env.BaseURL != "", "api_key": m.env.APIKey != "",
			"chat_model": m.env.ChatModel != "", "agent_model": m.env.AgentModel != ""},
	}
}

func validBaseURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("API 地址要以 http:// 或 https:// 开头，例如 https://api.openai.com/v1")
	}
	return nil
}

// merged 把输入和已保存的值合并（不落盘），用于保存和测试连接。
func (m *modelConf) merged(in SettingsInput) (ModelSettings, string, error) {
	s := ModelSettings{BaseURL: strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"),
		ChatModel: strings.TrimSpace(in.ChatModel), AgentModel: strings.TrimSpace(in.AgentModel)}
	m.mu.RLock()
	eff := m.eff
	m.mu.RUnlock()
	if m.env.BaseURL != "" {
		s.BaseURL = eff.baseURL
	}
	if m.env.AgentModel != "" {
		s.AgentModel = eff.agentModel
	}
	if m.env.ChatModel != "" {
		s.ChatModel = eff.chatModel
	}
	if err := validBaseURL(s.BaseURL); err != nil {
		return s, "", err
	}
	if s.AgentModel == "" {
		return s, "", errors.New("请填写模型名，例如 gpt-4o-mini、grok-3-mini、deepseek-chat")
	}
	key := strings.TrimSpace(in.APIKey)
	if key == "" && !in.ClearAPIKey {
		key = eff.apiKey
	}
	if m.env.APIKey != "" {
		key = m.env.APIKey
	}
	return s, key, nil
}

// Save 保存设置并立即生效（不用重启）。
func (m *modelConf) Save(in SettingsInput) error {
	s, _, err := m.merged(in)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(in.APIKey)
	if m.env.APIKey == "" {
		if key != "" {
			if err := m.vault.Set(modelKeyName, key, "模型 API Key（网页设置）", nil); err != nil {
				return err
			}
		} else if in.ClearAPIKey && m.vault.Has(modelKeyName) {
			if err := m.vault.Delete(modelKeyName); err != nil {
				return err
			}
		}
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	tmp := filepath.Join(m.dir, "model.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(m.dir, "model.json")); err != nil {
		return err
	}
	m.mu.Lock()
	m.saved = s
	m.rebuild()
	m.mu.Unlock()
	return nil
}

// Test 用输入的设置（空 Key = 已保存的）发一条很短的请求，确认地址、Key 和模型可用。
func (m *modelConf) Test(ctx context.Context, in SettingsInput) (string, error) {
	s, key, err := m.merged(in)
	if err != nil {
		return "", err
	}
	model := first(s.ChatModel, s.AgentModel)
	p := onefrog.NewOpenAI(s.BaseURL, key)
	p.MaxRetries = 0
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	msg, err := p.Chat(ctx, onefrog.ChatRequest{Model: model, Messages: []onefrog.Message{
		{Role: "user", Content: "Reply with the single word: pong"}}}, nil)
	if err != nil {
		e := err.Error()
		if key != "" {
			for _, v := range vault.Variants(key) {
				e = strings.ReplaceAll(e, v, "[已隐藏]")
			}
		}
		return "", fmt.Errorf("连接失败（模型 %s）：%s", model, onefrog.Truncate(e, 400))
	}
	return onefrog.Truncate(strings.TrimSpace(msg.Content), 200), nil
}

// RedactBytes 抹掉模型 API Key（以及它的常见编码）。
func (m *modelConf) RedactBytes(b []byte) []byte {
	b = m.vault.RedactBytes(b)
	if k := m.env.APIKey; k != "" {
		for _, v := range vault.Variants(k) {
			b = []byte(strings.ReplaceAll(string(b), v, "[已隐藏]"))
		}
	}
	return b
}

// switchProvider 每次调用时取当前生效的模型配置，所以网页里改了设置立即生效。
type switchProvider struct {
	m    *modelConf
	chat bool
}

func (p switchProvider) Chat(ctx context.Context, req onefrog.ChatRequest, onText func(string)) (onefrog.Message, error) {
	prov, model, err := p.m.current(p.chat)
	if err != nil {
		return onefrog.Message{}, err
	}
	if req.Model == "" {
		req.Model = model
	}
	return prov.Chat(ctx, req, onText)
}
