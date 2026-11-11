package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"frogbot/internal/onefrog"
	"frogbot/internal/vault"
)

// API 渠道是整台设备共用的：所有 Bot 从同一个渠道池（data/api-channels.json，密钥用默认 Bot 的主密钥加密）
// 里选择聊天模型和 Agent 模型。每个 Bot 只在自己的 channels.json 里记下「用哪个渠道的哪个模型」。

type APIChannel struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url"`
	Enabled      bool     `json:"enabled"`
	AuthRequired bool     `json:"auth_required"`
	APIKey       *string  `json:"api_key,omitempty"`
	HasAPIKey    bool     `json:"has_api_key"`
	Models       []string `json:"models"`
	HiddenModels []string `json:"hidden_models"`
}

type storedChannel struct {
	APIChannel
	Sealed   string `json:"sealed_api_key,omitempty"`
	key      string
	provider onefrog.Provider
}

// channelsView 是 GET /api/channels 的结果：设备上的全部渠道，以及每个 Bot 用了哪个渠道（界面上标注用途）。
type channelsView struct {
	Providers []APIChannel `json:"providers"`
	Usage     []channelUse `json:"usage"`
}

type channelUse struct {
	BotID           string `json:"bot_id"`
	Name            string `json:"name"`
	ChatProviderID  string `json:"chat_provider_id,omitempty"`
	AgentProviderID string `json:"agent_provider_id,omitempty"`
	AgentCLI        string `json:"agent_cli,omitempty"`
}

// channelPool 是设备上所有 Bot 共用的 API 渠道。
type channelPool struct {
	mu       sync.RWMutex
	path     string
	dataDir  string
	vault    *vault.Vault
	channels []storedChannel
}

type poolFile struct {
	Providers []storedChannel `json:"providers"`
}

// channelFile 是每个 Bot 自己的模型选择。Providers 只出现在旧版本写的文件里（那时渠道跟着 Bot 走），
// 读到时会并入共用渠道池，然后改写成只有选择的格式。
type channelFile struct {
	Providers       []storedChannel `json:"providers,omitempty"`
	DefaultID       string          `json:"default_provider_id"`
	Model           string          `json:"model"`
	AgentProviderID string          `json:"agent_provider_id,omitempty"`
	AgentModel      string          `json:"agent_model,omitempty"`
}

func openPool(dataDir string, v *vault.Vault) (*channelPool, error) {
	p := &channelPool{path: filepath.Join(dataDir, "api-channels.json"), dataDir: dataDir, vault: v}
	raw, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	var f poolFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s 格式不对: %w", p.path, err)
	}
	for i := range f.Providers {
		if f.Providers[i].Sealed != "" {
			if f.Providers[i].key, err = v.OpenAPIKey(f.Providers[i].Sealed); err != nil {
				return nil, err
			}
		}
		f.Providers[i].provider = newProvider(f.Providers[i].BaseURL, f.Providers[i].key)
	}
	p.channels = f.Providers
	return p, nil
}

func (p *channelPool) exists() bool {
	_, err := os.Stat(p.path)
	return err == nil
}

func (p *channelPool) public() []APIChannel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.publicLocked()
}

func (p *channelPool) publicLocked() []APIChannel {
	channels := make([]APIChannel, 0, len(p.channels))
	for _, s := range p.channels {
		c := s.APIChannel
		c.APIKey = nil
		c.HasAPIKey = s.key != ""
		c.Models, c.HiddenModels = slices.Clone(c.Models), slices.Clone(c.HiddenModels)
		channels = append(channels, c)
	}
	return channels
}

func (p *channelPool) find(id string) (storedChannel, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, c := range p.channels {
		if c.ID == id {
			return c, true
		}
	}
	return storedChannel{}, false
}

// key 返回渠道已保存的密钥，但只在渠道 ID 和地址都没变时（改了地址就不会把旧 Key 发给新地址）。
func (p *channelPool) key(id, url string) string {
	if c, ok := p.find(id); ok && c.BaseURL == url {
		return c.key
	}
	return ""
}

// save 用网页提交的完整渠道列表替换渠道池。APIKey 为 nil 时沿用同一渠道、同一地址的旧密钥；空字符串清除。
func (p *channelPool) save(list []APIChannel) ([]APIChannel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(list) > 32 {
		return nil, errors.New("最多添加 32 个渠道")
	}
	old := map[string]storedChannel{}
	for _, c := range p.channels {
		old[c.ID] = c
	}
	next := make([]storedChannel, 0, len(list))
	seen := map[string]bool{}
	for _, c := range list {
		c.ID, c.Name = strings.TrimSpace(c.ID), strings.TrimSpace(c.Name)
		if c.ID == "" || c.Name == "" || seen[c.ID] {
			return nil, errors.New("渠道名称和唯一标识不能为空或重复")
		}
		seen[c.ID] = true
		url, err := normalizeBaseURL(c.BaseURL)
		if err != nil {
			return nil, err
		}
		c.BaseURL = url
		key := ""
		if c.APIKey != nil {
			key = strings.TrimSpace(*c.APIKey)
		} else if prev, ok := old[c.ID]; ok && prev.BaseURL == url {
			key = prev.key
		}
		if !c.AuthRequired {
			key = ""
		}
		c.APIKey, c.HasAPIKey = nil, key != ""
		if c.Models == nil {
			c.Models = []string{}
		}
		if c.HiddenModels == nil {
			c.HiddenModels = []string{}
		}
		s := storedChannel{APIChannel: c, key: key, provider: newProvider(url, key)}
		if key != "" {
			if s.Sealed, err = p.vault.SealAPIKey(key); err != nil {
				return nil, err
			}
		}
		next = append(next, s)
	}
	if err := p.writeLocked(next); err != nil {
		return nil, err
	}
	p.channels = next
	return p.publicLocked(), nil
}

func (p *channelPool) writeLocked(channels []storedChannel) error {
	raw, err := json.MarshalIndent(poolFile{Providers: channels}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(p.dataDir, p.path, raw)
}

// merge 把旧版本里跟着某个 Bot 保存的渠道并入渠道池（密钥已解密）。同 ID 同地址的渠道视为同一个；
// ID 冲突但地址不同时换一个新 ID。返回旧 ID → 渠道池里 ID 的对应关系。
func (p *channelPool) merge(incoming []storedChannel) (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := map[string]string{}
	next := slices.Clone(p.channels)
	for _, c := range incoming {
		idx := slices.IndexFunc(next, func(x storedChannel) bool { return x.ID == c.ID })
		if idx >= 0 && next[idx].BaseURL == c.BaseURL && next[idx].key == c.key {
			ids[c.ID] = c.ID
			for _, m := range c.Models { // 两边各自拉取过的模型合在一起
				if !slices.Contains(next[idx].Models, m) {
					next[idx].Models = append(slices.Clone(next[idx].Models), m)
				}
			}
			continue
		}
		oldID := c.ID
		if idx >= 0 {
			c.ID = onefrog.NewID("ch")
			c.Name += "（迁移）"
		}
		var err error
		if c.Sealed = ""; c.key != "" {
			if c.Sealed, err = p.vault.SealAPIKey(c.key); err != nil {
				return nil, err
			}
		}
		c.HasAPIKey, c.APIKey = c.key != "", nil
		c.provider = newProvider(c.BaseURL, c.key)
		next = append(next, c)
		ids[oldID] = c.ID
	}
	if len(next) > 32 {
		return nil, errors.New("迁移后渠道超过 32 个，请先删除一些渠道")
	}
	if err := p.writeLocked(next); err != nil {
		return nil, err
	}
	p.channels = next
	return ids, nil
}

func writePrivate(dir, path string, raw []byte) error {
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	chmodErr := tmp.Chmod(0o600)
	_, writeErr := tmp.Write(raw)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err = errors.Join(chmodErr, writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("保存 %s 失败: %w", filepath.Base(path), err)
	}
	return os.Rename(tmp.Name(), path)
}

// ---------- 每个 Bot 的模型选择 ----------

func (b *Bot) channelPath() string { return filepath.Join(b.cfg.DataDir, "channels.json") }

func (b *Bot) loadChannels() error {
	raw, err := os.ReadFile(b.channelPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f channelFile
	if err = json.Unmarshal(raw, &f); err != nil {
		return err
	}
	if len(f.Providers) > 0 {
		// 旧格式：渠道跟着 Bot 保存。解密后并入共用渠道池，再把这个文件改写成只有选择。
		for i := range f.Providers {
			if f.Providers[i].Sealed != "" {
				if f.Providers[i].key, err = b.vault.OpenAPIKey(f.Providers[i].Sealed); err != nil {
					return err
				}
			}
		}
		ids, err := b.pool.merge(f.Providers)
		if err != nil {
			return fmt.Errorf("把 Bot 的 API 渠道并入共用渠道失败: %w", err)
		}
		f.DefaultID, f.AgentProviderID = ids[f.DefaultID], ids[f.AgentProviderID]
		f.Providers = nil
		if err := b.writeRoutes(f); err != nil {
			return err
		}
	}
	b.hasRoutes = true
	b.defaultChannel, b.chatModel, b.agentChannel, b.agentModelName = f.DefaultID, f.Model, f.AgentProviderID, f.AgentModel
	// Older versions shared one route; preserve that behavior on upgrade.
	if b.agentChannel == "" && b.agentModelName == "" {
		b.agentChannel, b.agentModelName = b.defaultChannel, b.chatModel
	}
	return nil
}

func (b *Bot) writeRoutes(f channelFile) error {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(b.cfg.DataDir, b.channelPath(), raw)
}

// catalogKey 决定这次请求用哪个 Key：请求里带了就用请求里的；否则只在渠道 ID 和地址都没变时
// 复用已保存的 Key。没指定渠道时按本 Bot 的聊天渠道算。
func (b *Bot) catalogKey(d APISettings, url string) string {
	if d.APIKey != nil {
		return strings.TrimSpace(*d.APIKey)
	}
	id := d.ProviderID
	if id == "" {
		b.modelMu.RLock()
		id = b.defaultChannel
		b.modelMu.RUnlock()
	}
	return b.pool.key(id, url)
}

// validRoute 检查渠道池里有这个已启用的渠道，并且模型在它的可选模型里。
func validRoute(channels []APIChannel, id, model string) bool {
	for _, c := range channels {
		if c.ID == id {
			return c.Enabled && model != "" && slices.Contains(c.Models, model) && !slices.Contains(c.HiddenModels, model)
		}
	}
	return false
}

// saveRoutes 保存本 Bot 用哪个渠道的哪个模型（不改渠道本身）。聊天可以留空（例如只用官方订阅 CLI）；
// Agent 留空时跟聊天相同。
func (b *Bot) saveRoutes(d APISettings) (APISettings, error) {
	channels := b.pool.public()
	chatID, chat := strings.TrimSpace(d.DefaultProviderID), strings.TrimSpace(d.Model)
	agentID, agent := strings.TrimSpace(d.AgentProviderID), strings.TrimSpace(d.AgentModel)
	if agentID == "" && agent == "" {
		agentID, agent = chatID, chat
	}
	if len(channels) == 0 {
		chatID, chat, agentID, agent = "", "", "", ""
	}
	if (chatID != "" || chat != "") && !validRoute(channels, chatID, chat) {
		return APISettings{}, errors.New("请为聊天选择已启用渠道中的可用模型")
	}
	if (agentID != "" || agent != "") && !validRoute(channels, agentID, agent) {
		return APISettings{}, errors.New("请为 Agent 选择已启用渠道中的可用模型")
	}
	b.modelMu.Lock()
	err := b.writeRoutes(channelFile{DefaultID: chatID, Model: chat, AgentProviderID: agentID, AgentModel: agent})
	if err == nil {
		b.hasRoutes = true
		b.defaultChannel, b.chatModel, b.agentChannel, b.agentModelName = chatID, chat, agentID, agent
	}
	b.modelMu.Unlock()
	if err != nil {
		return APISettings{}, err
	}
	return b.GetSettings(), nil
}

// saveChannels 是旧接口：同时保存渠道池（整台设备）和本 Bot 的选择。先校验选择，再写渠道池。
func (b *Bot) saveChannels(d APISettings) (APISettings, error) {
	preview := make([]APIChannel, 0, len(d.Providers))
	for _, c := range d.Providers {
		c.ID = strings.TrimSpace(c.ID)
		preview = append(preview, c)
	}
	if len(preview) > 0 {
		if !validRoute(preview, d.DefaultProviderID, strings.TrimSpace(d.Model)) {
			return APISettings{}, errors.New("请为默认模型选择已启用渠道中的可用模型")
		}
		if (d.AgentProviderID != "" || d.AgentModel != "") && !validRoute(preview, d.AgentProviderID, strings.TrimSpace(d.AgentModel)) {
			return APISettings{}, errors.New("请为 Agent 选择已启用渠道中的可用模型")
		}
	}
	if _, err := b.pool.save(d.Providers); err != nil {
		return APISettings{}, err
	}
	return b.saveRoutes(d)
}

func (b *Bot) TestConnection(ctx context.Context, d APISettings) (int64, error) {
	url, err := normalizeBaseURL(d.BaseURL)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(d.Model) == "" {
		return 0, errors.New("请选择测试模型")
	}
	key := b.catalogKey(d, url)
	p := onefrog.NewOpenAI(url, key)
	p.Client.CheckRedirect = noRedirect
	p.MaxRetries = 0
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	start := time.Now()
	_, err = p.Chat(ctx, onefrog.ChatRequest{Model: d.Model, Messages: []onefrog.Message{{Role: "user", Content: "Reply OK."}}}, nil)
	if err != nil {
		return 0, errors.New("测试连接失败，请检查接口、密钥和模型是否可用")
	}
	return time.Since(start).Milliseconds(), nil
}
