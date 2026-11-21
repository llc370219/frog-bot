package agy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Config 配置直连 agy 模块的数据目录、默认项目根目录以及进程沙箱包装器。
type Config struct {
	DataDir      string                // frog 数据目录（保存 agy-projects.json / agy-sessions.json；禁止作为项目目录）
	ProjectsRoot string                // 新建托管项目文件夹的默认父目录（例如 <workspace>-agy）
	WrapCmd      func(*exec.Cmd) error // 可选：Linux Landlock 沙箱包装（阻止读取 DataDir 里的密钥）
}

// Project 是一个给 agy 工作的项目文件夹（由 frog 创建在 ProjectsRoot 下，或关联服务器上已有目录）。
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Managed   bool      `json:"managed"` // true 表示位于 ProjectsRoot 下，删除项目时可选一并删除目录
	CreatedAt time.Time `json:"created_at"`
}

// ModeOption 描述 agy 支持的执行权限模式。
type ModeOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ModelOption 描述 agy 支持的模型选项。
type ModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StatusView 返回服务器上 agy CLI 的可用性与缓存的模型/模式列表。
type StatusView struct {
	Installed    bool            `json:"installed"`
	Path         string          `json:"path,omitempty"`
	AgentVersion string          `json:"agent_version,omitempty"`
	ProjectsRoot string          `json:"projects_root"`
	Modes        []ModeOption    `json:"modes"`
	Models       []ModelOption   `json:"models"`
	Commands     []CommandOption `json:"commands,omitempty"`
	DefaultMode  string          `json:"default_mode"`
	DefaultModel string          `json:"default_model,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// CommandOption 描述 agy 通过 available_commands_update 上报的可用斜杠命令。
type CommandOption struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"input_hint,omitempty"`
}

// QueuedPrompt 描述忙碌期间排队等待（follow_up）或待插话（steer）的用户消息。
type QueuedPrompt struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Behavior  string    `json:"behavior,omitempty"` // follow_up | steer
	CreatedAt time.Time `json:"created_at"`
}

// SendResult 是发送消息的即时响应：空闲时立刻开跑，忙碌时返回排队/插话状态。
type SendResult struct {
	Queued    bool          `json:"queued"`
	Behavior  string        `json:"behavior,omitempty"`
	Message   *ChatMessage  `json:"message,omitempty"`
	QueueItem *QueuedPrompt `json:"queue_item,omitempty"`
}

// SessionSummary 是会话列表里的一条记录。
type SessionSummary struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	Cwd                string    `json:"cwd"`
	Title              string    `json:"title"`
	Mode               string    `json:"mode"`
	Model              string    `json:"model,omitempty"`
	Busy               bool      `json:"busy"`
	PendingPermissions int       `json:"pending_permissions"`
	QueuedCount        int       `json:"queued_count"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ChatMessage 是对话时间线里的一条用户或助理消息。
type ChatMessage struct {
	ID         string     `json:"id"`
	Role       string     `json:"role"` // user | assistant
	Text       string     `json:"text"`
	Thinking   string     `json:"thinking,omitempty"`
	ThinkingMs int64      `json:"thinking_ms,omitempty"`
	Tools      []ToolCall `json:"tools,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ToolCall 记录 agy 在单轮中执行的一次工具调用（读文件、改文件、运行命令、搜索等）。
type ToolCall struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Title  string `json:"title,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"` // pending | in_progress | completed | failed
	Detail string `json:"detail,omitempty"` // 入参概要（命令/路径/关键词）
	Output string `json:"output,omitempty"` // 执行结果或变更摘要
}

// PermissionReq 是 agy 在非全自动模式下发出的确认请求。
type PermissionReq struct {
	ID        string       `json:"id"`
	SessionID string       `json:"session_id"`
	ToolName  string       `json:"tool_name,omitempty"`
	Title     string       `json:"title"`
	Kind      string       `json:"kind,omitempty"`
	Detail    string       `json:"detail,omitempty"`
	Options   []PermOption `json:"options"`
	Status    string       `json:"status"` // pending | decided | cancelled
	ChosenID  string       `json:"chosen_id,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

// Snapshot 是打开某个对话 SSE 时首帧推送的完整实时状态。
type Snapshot struct {
	Session     SessionSummary  `json:"session"`
	Messages    []ChatMessage   `json:"messages"`
	Permissions []PermissionReq `json:"permissions"`
	Queue       []QueuedPrompt  `json:"queue,omitempty"`
	Streaming   string          `json:"streaming"`
	Thinking    string          `json:"thinking"`
	ThinkStart  int64           `json:"think_start,omitempty"`
	ThinkEnd    int64           `json:"think_end,omitempty"`
	LiveTools   []ToolCall      `json:"live_tools,omitempty"`
	UsedTokens  int64           `json:"used_tokens,omitempty"`
	MaxTokens   int64           `json:"max_tokens,omitempty"`
	Modes       []ModeOption    `json:"modes"`
	Models      []ModelOption   `json:"models"`
	Commands    []CommandOption `json:"commands,omitempty"`
}

// StreamEvent 是 SSE 推送的单条事件。
type StreamEvent struct {
	Seq  int64     `json:"seq"`
	Kind string    `json:"kind"`
	Time time.Time `json:"time"`
	Data any       `json:"data"`
}

var defaultModes = []ModeOption{
	{ID: "auto", Name: "Auto（自动执行）", Description: "自动批准工作区修改与安全命令，仅高危不可逆操作询问"},
	{ID: "accept-edits", Name: "Accept Edits（自动改文件）", Description: "工作区内改文件免确认，执行命令按策略询问"},
	{ID: "plan", Name: "Plan（先规划）", Description: "先深入分析并制定方案，再动手修改"},
	{ID: "default", Name: "Default（默认询问）", Description: "修改文件与运行命令前均按权限配置确认"},
}

type savedSessionMeta struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Cwd       string    `json:"cwd"`
	Title     string    `json:"title,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	Model     string    `json:"model,omitempty"`
	Deleted   bool      `json:"deleted,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type pendingPerm struct {
	req PermissionReq
	ch  chan string
}

type sessionRuntime struct {
	id         string
	projectID  string
	cwd        string
	title      string
	mode       string
	model      string
	updatedAt  time.Time
	loaded     bool
	replaying  bool
	busy       bool
	msgs       []ChatMessage
	perms      []*pendingPerm
	queue      []QueuedPrompt
	streaming  strings.Builder
	thinking   strings.Builder
	thinkStart time.Time
	thinkEnd   time.Time
	liveTools  []ToolCall
	replayUser strings.Builder
	replayAsst strings.Builder
	replayThk  strings.Builder
	replayTls  []ToolCall
	usedTokens int64
	maxTokens  int64
	seq        int64
	subs       map[chan StreamEvent]struct{}
}

// Hub 管理项目文件夹、agy acp 守护连接以及所有活跃对话的流式广播。
type Hub struct {
	cfg       Config
	dataDir   string
	projRoot  string
	mu        sync.Mutex
	clientMu  sync.Mutex // 串行创建/重连 acpClient
	client    *acpClient
	projects  []Project
	meta      map[string]savedSessionMeta
	sessions  map[string]*sessionRuntime
	modes     []ModeOption
	models    []ModelOption
	commands  []CommandOption
	defMode   string
	defModel  string
	agentVer  string
	closed    bool
}

func NewHub(cfg Config) (*Hub, error) {
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	projRoot := cfg.ProjectsRoot
	if projRoot == "" {
		projRoot = filepath.Join(filepath.Dir(dataDir), "workspace-agy")
	}
	projRoot, err = filepath.Abs(projRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		return nil, err
	}
	h := &Hub{
		cfg:      cfg,
		dataDir:  dataDir,
		projRoot: projRoot,
		meta:     map[string]savedSessionMeta{},
		sessions: map[string]*sessionRuntime{},
		modes:    slices.Clone(defaultModes),
		defMode:  "auto",
	}
	_ = h.loadProjects()
	_ = h.loadMeta()
	if len(h.projects) == 0 {
		defDir := filepath.Join(projRoot, "default")
		if os.MkdirAll(defDir, 0o755) == nil {
			h.projects = []Project{{
				ID:        newShortID("proj"),
				Name:      "默认项目",
				Path:      realOrClean(defDir),
				Managed:   true,
				CreatedAt: time.Now(),
			}}
			_ = h.saveProjectsLocked()
		}
	}
	return h, nil
}

func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	c := h.client
	h.client = nil
	for _, rt := range h.sessions {
		for _, p := range rt.perms {
			if p.req.Status == "pending" {
				p.req.Status = "cancelled"
				select {
				case p.ch <- "":
				default:
				}
			}
		}
		for ch := range rt.subs {
			close(ch)
			delete(rt.subs, ch)
		}
	}
	h.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// ---------- 项目文件夹管理 ----------

var folderSlugRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._-]{0,63}$`)

func (h *Hub) ListProjects() []Project {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.projects)
}

func (h *Hub) FindProject(id string) (Project, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// CreateProject 新建托管项目目录（customPath 为空）或关联服务器上已有目录（customPath 非空）。
func (h *Hub) CreateProject(name, customPath string) (Project, error) {
	name = strings.TrimSpace(name)
	customPath = strings.TrimSpace(customPath)

	var target string
	managed := false
	if customPath == "" {
		if !folderSlugRe.MatchString(name) {
			return Project{}, errors.New("文件夹名需为 1～64 个字母、数字、中文或 . _ -，且不能以符号开头")
		}
		target = filepath.Join(h.projRoot, name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return Project{}, fmt.Errorf("创建项目文件夹失败：%w", err)
		}
		target = realOrClean(target)
		managed = true
	} else {
		if strings.HasPrefix(customPath, "~/") || customPath == "~" {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				customPath = filepath.Join(home, strings.TrimPrefix(customPath, "~"))
			}
		}
		abs, err := filepath.Abs(customPath)
		if err != nil {
			return Project{}, fmt.Errorf("目录路径无效：%w", err)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			st, statErr := os.Stat(abs)
			if statErr != nil || !st.IsDir() {
				return Project{}, fmt.Errorf("目录不存在且无法创建：%v", err)
			}
		}
		target = realOrClean(abs)
		st, err := os.Stat(target)
		if err != nil || !st.IsDir() {
			return Project{}, errors.New("指定路径不是有效文件夹")
		}
		if isWithinDir(target, realOrClean(h.dataDir)) {
			return Project{}, errors.New("出于安全原因，不能将 frog 自身的数据目录设为项目文件夹")
		}
		managed = isWithinDir(target, realOrClean(h.projRoot)) && target != realOrClean(h.projRoot)
		if name == "" {
			name = filepath.Base(target)
		}
	}
	if utf8.RuneCountInString(name) == 0 || utf8.RuneCountInString(name) > 64 {
		return Project{}, errors.New("项目名称需为 1～64 个字符")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, existing := range h.projects {
		if existing.Path == target {
			return existing, nil
		}
	}
	p := Project{
		ID:        newShortID("proj"),
		Name:      name,
		Path:      target,
		Managed:   managed,
		CreatedAt: time.Now(),
	}
	h.projects = append(h.projects, p)
	if err := h.saveProjectsLocked(); err != nil {
		h.projects = h.projects[:len(h.projects)-1]
		return Project{}, err
	}
	return p, nil
}

func (h *Hub) DeleteProject(id string, deleteFiles bool) error {
	h.mu.Lock()
	idx := slices.IndexFunc(h.projects, func(p Project) bool { return p.ID == id })
	if idx < 0 {
		h.mu.Unlock()
		return errors.New("项目不存在")
	}
	p := h.projects[idx]
	var sessIDs []string
	for sid, rt := range h.sessions {
		if rt.projectID == id || rt.cwd == p.Path {
			sessIDs = append(sessIDs, sid)
		}
	}
	h.projects = slices.Delete(slices.Clone(h.projects), idx, idx+1)
	if err := h.saveProjectsLocked(); err != nil {
		h.mu.Unlock()
		return err
	}
	c := h.client
	h.mu.Unlock()

	if c != nil && c.alive() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		for _, sid := range sessIDs {
			_ = c.call(ctx, "session/close", map[string]any{"sessionId": sid}, nil)
		}
		cancel()
	}
	if deleteFiles && p.Managed {
		root := realOrClean(h.projRoot)
		dir := realOrClean(p.Path)
		if filepath.Dir(dir) == root && dir != root {
			_ = os.RemoveAll(dir)
		}
	}
	return nil
}

func (h *Hub) projectsPath() string { return filepath.Join(h.dataDir, "agy-projects.json") }
func (h *Hub) metaPath() string     { return filepath.Join(h.dataDir, "agy-sessions.json") }

func (h *Hub) loadProjects() error {
	raw, err := os.ReadFile(h.projectsPath())
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, &h.projects)
}

func (h *Hub) saveProjectsLocked() error {
	return writeAtomicJSON(h.projectsPath(), h.projects)
}

func (h *Hub) loadMeta() error {
	raw, err := os.ReadFile(h.metaPath())
	if err != nil {
		return err
	}
	var list []savedSessionMeta
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	for _, m := range list {
		h.meta[m.ID] = m
	}
	return nil
}

func (h *Hub) saveMetaLocked() {
	list := make([]savedSessionMeta, 0, len(h.meta))
	for _, m := range h.meta {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
	if len(list) > 500 {
		list = list[:500]
	}
	_ = writeAtomicJSON(h.metaPath(), list)
}

// ---------- ACP 连接管理 ----------

func (h *Hub) ensureClient() (*acpClient, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("服务正在关闭")
	}
	if h.client != nil && h.client.alive() {
		c := h.client
		h.mu.Unlock()
		return c, nil
	}
	h.mu.Unlock()

	h.clientMu.Lock()
	defer h.clientMu.Unlock()

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("服务正在关闭")
	}
	if h.client != nil && h.client.alive() {
		c := h.client
		h.mu.Unlock()
		return c, nil
	}
	h.mu.Unlock()

	exe, err := ResolveBinary()
	if err != nil {
		return nil, err
	}
	var created *acpClient
	created, err = startACPClient(exe, h.cfg.WrapCmd, h.handleUpdate, h.handlePermission, func(_ error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.client == created {
			h.client = nil
		}
		for _, rt := range h.sessions {
			rt.loaded = false
		}
	})
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		created.Close()
		return nil, errors.New("服务正在关闭")
	}
	h.client = created
	if created.agentVer != "" {
		h.agentVer = created.agentVer
	}
	h.mu.Unlock()
	return created, nil
}

// Status 检测服务器上的 agy CLI 状态并返回模式与模型列表。
func (h *Hub) Status(ctx context.Context) StatusView {
	exe, err := ResolveBinary()
	h.mu.Lock()
	modes := slices.Clone(h.modes)
	models := slices.Clone(h.models)
	commands := slices.Clone(h.commands)
	defMode := h.defMode
	defModel := h.defModel
	ver := h.agentVer
	root := h.projRoot
	h.mu.Unlock()

	view := StatusView{
		Installed:    err == nil,
		Path:         exe,
		AgentVersion: ver,
		ProjectsRoot: root,
		Modes:        modes,
		Models:       models,
		Commands:     commands,
		DefaultMode:  defMode,
		DefaultModel: defModel,
	}
	if err != nil {
		view.Error = err.Error()
		return view
	}
	c, cerr := h.ensureClient()
	if cerr != nil {
		view.Error = cerr.Error()
		return view
	}
	if c.agentVer != "" {
		view.AgentVersion = c.agentVer
	}
	return view
}

// ---------- 会话（对话）管理 ----------

type acpConfigOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Value       string `json:"value"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	} `json:"options"`
}

type acpCommandWire struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Input       *struct {
		Hint string `json:"hint,omitempty"`
	} `json:"input,omitempty"`
}

type acpSessionMetaResp struct {
	SessionID string `json:"sessionId"`
	Models    struct {
		CurrentModelID  string `json:"currentModelId"`
		AvailableModels []struct {
			ModelID string `json:"modelId"`
			Name    string `json:"name"`
		} `json:"availableModels"`
	} `json:"models"`
	Modes struct {
		CurrentModeID  string       `json:"currentModeId"`
		AvailableModes []ModeOption `json:"availableModes"`
	} `json:"modes"`
	ConfigOptions     []acpConfigOption `json:"configOptions"`
	AvailableCommands []acpCommandWire  `json:"availableCommands"`
}

func (h *Hub) captureCapabilitiesLocked(resp acpSessionMetaResp) {
	if len(resp.Modes.AvailableModes) > 0 {
		h.modes = resp.Modes.AvailableModes
	}
	if len(resp.Models.AvailableModels) > 0 {
		models := make([]ModelOption, 0, len(resp.Models.AvailableModels))
		for _, m := range resp.Models.AvailableModels {
			if m.ModelID != "" {
				name := m.Name
				if name == "" {
					name = m.ModelID
				}
				models = append(models, ModelOption{ID: m.ModelID, Name: name})
			}
		}
		if len(models) > 0 {
			h.models = models
		}
	}
	for _, cfg := range resp.ConfigOptions {
		cat := strings.ToLower(cfg.Category)
		if cat == "" {
			cat = strings.ToLower(cfg.ID)
		}
		switch cat {
		case "mode", "permission_mode":
			if len(cfg.Options) > 0 {
				modes := make([]ModeOption, 0, len(cfg.Options))
				for _, o := range cfg.Options {
					if o.Value != "" {
						name := o.Name
						if name == "" {
							name = o.Value
						}
						modes = append(modes, ModeOption{ID: o.Value, Name: name, Description: o.Description})
					}
				}
				if len(modes) > 0 {
					h.modes = modes
				}
			}
		case "model":
			if len(cfg.Options) > 0 {
				models := make([]ModelOption, 0, len(cfg.Options))
				for _, o := range cfg.Options {
					if o.Value != "" {
						name := o.Name
						if name == "" {
							name = o.Value
						}
						models = append(models, ModelOption{ID: o.Value, Name: name})
					}
				}
				if len(models) > 0 {
					h.models = models
				}
			}
			if cfg.CurrentValue != "" && h.defModel == "" {
				h.defModel = cfg.CurrentValue
			}
		}
	}
	if resp.Models.CurrentModelID != "" && h.defModel == "" {
		h.defModel = resp.Models.CurrentModelID
	}
	if len(resp.AvailableCommands) > 0 {
		h.commands = toCommandOptions(resp.AvailableCommands)
	}
}

func toCommandOptions(wire []acpCommandWire) []CommandOption {
	out := make([]CommandOption, 0, len(wire))
	for _, c := range wire {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		hint := ""
		if c.Input != nil {
			hint = strings.TrimSpace(c.Input.Hint)
		}
		out = append(out, CommandOption{
			Name:        name,
			Description: strings.TrimSpace(c.Description),
			InputHint:   hint,
		})
	}
	return out
}

func (h *Hub) ListSessions(ctx context.Context, projectID string) ([]SessionSummary, error) {
	var proj *Project
	if projectID != "" {
		p, ok := h.FindProject(projectID)
		if !ok {
			return nil, errors.New("项目不存在")
		}
		proj = &p
	}
	c, err := h.ensureClient()
	if err != nil {
		return nil, err
	}
	cwd := h.projRoot
	if proj != nil {
		cwd = proj.Path
	}
	var listRes struct {
		Sessions []struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
			Title     string `json:"title"`
			UpdatedAt string `json:"updatedAt"`
		} `json:"sessions"`
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.call(callCtx, "session/list", map[string]any{"cwd": cwd}, &listRes); err != nil {
		return nil, fmt.Errorf("读取对话列表失败：%w", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	byPath := map[string]string{}
	for _, p := range h.projects {
		byPath[realOrClean(p.Path)] = p.ID
	}
	wantPath := ""
	if proj != nil {
		wantPath = realOrClean(proj.Path)
	}

	seen := map[string]bool{}
	var out []SessionSummary
	for _, item := range listRes.Sessions {
		if item.SessionID == "" {
			continue
		}
		if m, ok := h.meta[item.SessionID]; ok && m.Deleted {
			continue
		}
		cleanCwd := realOrClean(item.Cwd)
		if wantPath != "" && cleanCwd != wantPath {
			continue
		}
		seen[item.SessionID] = true
		updated, _ := time.Parse(time.RFC3339, item.UpdatedAt)
		if updated.IsZero() {
			updated = time.Now()
		}
		pid := byPath[cleanCwd]
		if pid == "" && proj != nil {
			pid = proj.ID
		}
		s := SessionSummary{
			ID:        item.SessionID,
			ProjectID: pid,
			Cwd:       cleanCwd,
			Title:     strings.TrimSpace(item.Title),
			Mode:      h.defMode,
			Model:     h.defModel,
			UpdatedAt: updated,
		}
		if m, ok := h.meta[item.SessionID]; ok {
			if m.Title != "" {
				s.Title = m.Title
			}
			if m.Mode != "" {
				s.Mode = m.Mode
			}
			if m.Model != "" {
				s.Model = m.Model
			}
			if m.UpdatedAt.After(s.UpdatedAt) {
				s.UpdatedAt = m.UpdatedAt
			}
		}
		if rt := h.sessions[item.SessionID]; rt != nil {
			s = h.summaryFromRuntimeLocked(rt)
		}
		if s.Title == "" {
			s.Title = "新对话"
		}
		out = append(out, s)
	}
	// 合并刚新建、还没发过第一条消息（因此可能尚未出现在 agy 索引里）的会话
	for id, m := range h.meta {
		if m.Deleted || seen[id] {
			continue
		}
		if wantPath != "" && realOrClean(m.Cwd) != wantPath {
			continue
		}
		if rt := h.sessions[id]; rt != nil {
			out = append(out, h.summaryFromRuntimeLocked(rt))
		} else {
			title := m.Title
			if title == "" {
				title = "新对话"
			}
			mode := m.Mode
			if mode == "" {
				mode = h.defMode
			}
			out = append(out, SessionSummary{
				ID:        m.ID,
				ProjectID: m.ProjectID,
				Cwd:       m.Cwd,
				Title:     title,
				Mode:      mode,
				Model:     m.Model,
				UpdatedAt: m.UpdatedAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (h *Hub) summaryFromRuntimeLocked(rt *sessionRuntime) SessionSummary {
	pending := 0
	for _, p := range rt.perms {
		if p.req.Status == "pending" {
			pending++
		}
	}
	title := rt.title
	if title == "" {
		title = "新对话"
	}
	mode := rt.mode
	if mode == "" {
		mode = h.defMode
	}
	return SessionSummary{
		ID:                 rt.id,
		ProjectID:          rt.projectID,
		Cwd:                rt.cwd,
		Title:              title,
		Mode:               mode,
		Model:              rt.model,
		Busy:               rt.busy,
		PendingPermissions: pending,
		QueuedCount:        len(rt.queue),
		UpdatedAt:          rt.updatedAt,
	}
}

// CreateSession 在指定项目文件夹下新建一个 agy 对话。
func (h *Hub) CreateSession(ctx context.Context, projectID, mode, model string) (Snapshot, error) {
	proj, ok := h.FindProject(projectID)
	if !ok {
		projects := h.ListProjects()
		if len(projects) == 0 {
			return Snapshot{}, errors.New("请先创建或关联一个项目文件夹")
		}
		proj = projects[0]
	}
	c, err := h.ensureClient()
	if err != nil {
		return Snapshot{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var res acpSessionMetaResp
	if err := c.call(callCtx, "session/new", map[string]any{
		"cwd":        proj.Path,
		"mcpServers": []any{},
	}, &res); err != nil {
		return Snapshot{}, fmt.Errorf("新建 agy 对话失败：%w", err)
	}
	if res.SessionID == "" {
		return Snapshot{}, errors.New("agy 未返回会话 ID")
	}

	wantMode := strings.TrimSpace(mode)
	if wantMode == "" {
		wantMode = h.defMode
	}
	curMode := res.Modes.CurrentModeID
	if wantMode != "" && wantMode != curMode {
		if c.call(callCtx, "session/set_mode", map[string]any{"sessionId": res.SessionID, "modeId": wantMode}, nil) == nil {
			curMode = wantMode
		}
	}
	if curMode == "" {
		curMode = "auto"
	}

	wantModel := strings.TrimSpace(model)
	curModel := res.Models.CurrentModelID
	if wantModel != "" && wantModel != curModel {
		if c.call(callCtx, "session/set_model", map[string]any{"sessionId": res.SessionID, "modelId": wantModel}, nil) == nil {
			curModel = wantModel
		}
	}

	now := time.Now()
	h.mu.Lock()
	h.captureCapabilitiesLocked(res)
	rt := h.getOrCreateRuntimeLocked(res.SessionID, proj.ID, proj.Path)
	rt.loaded = true
	rt.title = "新对话"
	rt.mode = curMode
	rt.model = curModel
	rt.updatedAt = now
	h.meta[res.SessionID] = savedSessionMeta{
		ID:        res.SessionID,
		ProjectID: proj.ID,
		Cwd:       proj.Path,
		Title:     rt.title,
		Mode:      curMode,
		Model:     curModel,
		UpdatedAt: now,
	}
	h.saveMetaLocked()
	snap := h.snapshotLocked(rt)
	h.mu.Unlock()
	return snap, nil
}

func (h *Hub) getOrCreateRuntimeLocked(id, projectID, cwd string) *sessionRuntime {
	if rt := h.sessions[id]; rt != nil {
		if projectID != "" {
			rt.projectID = projectID
		}
		if cwd != "" {
			rt.cwd = cwd
		}
		return rt
	}
	if m, ok := h.meta[id]; ok {
		if projectID == "" {
			projectID = m.ProjectID
		}
		if cwd == "" {
			cwd = m.Cwd
		}
	}
	if cwd == "" {
		for _, p := range h.projects {
			if p.ID == projectID {
				cwd = p.Path
				break
			}
		}
	}
	if cwd == "" && len(h.projects) > 0 {
		projectID = h.projects[0].ID
		cwd = h.projects[0].Path
	}
	rt := &sessionRuntime{
		id:        id,
		projectID: projectID,
		cwd:       cwd,
		mode:      h.defMode,
		model:     h.defModel,
		updatedAt: time.Now(),
		subs:      map[chan StreamEvent]struct{}{},
	}
	if m, ok := h.meta[id]; ok {
		if m.Title != "" {
			rt.title = m.Title
		}
		if m.Mode != "" {
			rt.mode = m.Mode
		}
		if m.Model != "" {
			rt.model = m.Model
		}
		if !m.UpdatedAt.IsZero() {
			rt.updatedAt = m.UpdatedAt
		}
	}
	h.sessions[id] = rt
	return rt
}

// ensureLoaded 确保指定会话已经在当前 acpClient 中加载（首次打开时通过 session/load 回放历史记录）。
func (h *Hub) ensureLoaded(ctx context.Context, sessionID, projectID string) (*acpClient, *sessionRuntime, error) {
	c, err := h.ensureClient()
	if err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	rt := h.getOrCreateRuntimeLocked(sessionID, projectID, "")
	if rt.loaded {
		h.mu.Unlock()
		return c, rt, nil
	}
	cwd := rt.cwd
	savedMode := rt.mode
	savedModel := rt.model
	rt.replaying = true
	rt.msgs = nil
	rt.replayUser.Reset()
	rt.replayAsst.Reset()
	rt.replayThk.Reset()
	rt.replayTls = nil
	h.mu.Unlock()

	loadCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var res acpSessionMetaResp
	err = c.call(loadCtx, "session/load", map[string]any{
		"sessionId":  sessionID,
		"cwd":        cwd,
		"mcpServers": []any{},
	}, &res)

	h.mu.Lock()
	h.flushReplayTurnLocked(rt, true)
	rt.replaying = false
	if err != nil {
		h.mu.Unlock()
		return nil, nil, fmt.Errorf("加载对话失败：%w", err)
	}
	h.captureCapabilitiesLocked(res)
	rt.loaded = true
	if res.Modes.CurrentModeID != "" {
		rt.mode = res.Modes.CurrentModeID
	}
	if res.Models.CurrentModelID != "" {
		rt.model = res.Models.CurrentModelID
	}
	h.mu.Unlock()

	if savedMode != "" && savedMode != rt.mode {
		if c.call(loadCtx, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": savedMode}, nil) == nil {
			h.mu.Lock()
			rt.mode = savedMode
			h.mu.Unlock()
		}
	}
	if savedModel != "" && savedModel != rt.model {
		if c.call(loadCtx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": savedModel}, nil) == nil {
			h.mu.Lock()
			rt.model = savedModel
			h.mu.Unlock()
		}
	}
	return c, rt, nil
}

// UpdateSessionConfig 切换某个对话的 agy 权限模式、模型或标题。
func (h *Hub) UpdateSessionConfig(ctx context.Context, sessionID, mode, model, title string) (SessionSummary, error) {
	c, rt, err := h.ensureLoaded(ctx, sessionID, "")
	if err != nil {
		return SessionSummary{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if mode = strings.TrimSpace(mode); mode != "" {
		if err := c.call(callCtx, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": mode}, nil); err != nil {
			return SessionSummary{}, fmt.Errorf("切换模式失败：%w", err)
		}
	}
	if model = strings.TrimSpace(model); model != "" {
		if err := c.call(callCtx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": model}, nil); err != nil {
			// 部分版本走 set_config_option
			if err2 := c.call(callCtx, "session/set_config_option", map[string]any{"sessionId": sessionID, "configId": "model", "value": model}, nil); err2 != nil {
				return SessionSummary{}, fmt.Errorf("切换模型失败：%w", err)
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if mode != "" {
		rt.mode = mode
		h.defMode = mode
	}
	if model != "" {
		rt.model = model
		h.defModel = model
	}
	if title = strings.TrimSpace(title); title != "" {
		rt.title = truncateRunes(title, 60)
	}
	m := h.meta[sessionID]
	m.ID = sessionID
	m.ProjectID = rt.projectID
	m.Cwd = rt.cwd
	m.Title = rt.title
	m.Mode = rt.mode
	m.Model = rt.model
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = rt.updatedAt
	}
	h.meta[sessionID] = m
	h.saveMetaLocked()
	sum := h.summaryFromRuntimeLocked(rt)
	h.broadcastLocked(rt, "session.meta", map[string]any{
		"session":     sum,
		"used_tokens": rt.usedTokens,
		"max_tokens":  rt.maxTokens,
	})
	return sum, nil
}

func (h *Hub) DeleteSession(ctx context.Context, sessionID string) error {
	_ = h.StopPrompt(sessionID)
	h.mu.Lock()
	c := h.client
	rt := h.sessions[sessionID]
	delete(h.sessions, sessionID)
	m := h.meta[sessionID]
	m.ID = sessionID
	m.Deleted = true
	m.UpdatedAt = time.Now()
	h.meta[sessionID] = m
	h.saveMetaLocked()
	if rt != nil {
		for ch := range rt.subs {
			close(ch)
			delete(rt.subs, ch)
		}
	}
	h.mu.Unlock()

	if c != nil && c.alive() {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = c.call(callCtx, "session/close", map[string]any{"sessionId": sessionID}, nil)
		cancel()
	}
	return nil
}

// ---------- 对话收发与事件流 ----------

// SendPrompt 发送一条指令给 agy：空闲时立刻执行；忙碌时按 Pi RPC 模式支持 follow_up 排队或 steer 打断插话。
func (h *Hub) SendPrompt(ctx context.Context, sessionID, projectID, text, behavior string) (SendResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return SendResult{}, errors.New("消息内容不能为空")
	}
	if behavior != "" && behavior != "follow_up" && behavior != "steer" {
		return SendResult{}, errors.New("behavior 只能为空、follow_up 或 steer")
	}
	c, rt, err := h.ensureLoaded(ctx, sessionID, projectID)
	if err != nil {
		return SendResult{}, err
	}

	now := time.Now()
	h.mu.Lock()
	if rt.busy {
		if behavior == "" {
			behavior = "follow_up"
		}
		item := QueuedPrompt{
			ID:        newShortID("qp"),
			Text:      text,
			Behavior:  behavior,
			CreatedAt: now,
		}
		if behavior == "steer" {
			// steer 插在普通 follow_up 之前（但保持多条 steer 的提交顺序）
			insertAt := 0
			for insertAt < len(rt.queue) && rt.queue[insertAt].Behavior == "steer" {
				insertAt++
			}
			rt.queue = slices.Insert(rt.queue, insertAt, item)
			for _, p := range rt.perms {
				if p.req.Status == "pending" {
					p.req.Status = "cancelled"
					select {
					case p.ch <- "":
					default:
					}
					h.broadcastLocked(rt, "permission.upsert", p.req)
				}
			}
		} else {
			rt.queue = append(rt.queue, item)
		}
		h.broadcastLocked(rt, "queue.update", slices.Clone(rt.queue))
		h.broadcastLocked(rt, "session.meta", map[string]any{
			"session":     h.summaryFromRuntimeLocked(rt),
			"used_tokens": rt.usedTokens,
			"max_tokens":  rt.maxTokens,
		})
		h.mu.Unlock()
		if behavior == "steer" && c != nil && c.alive() {
			_ = c.notify("session/cancel", map[string]any{"sessionId": sessionID})
		}
		return SendResult{Queued: true, Behavior: behavior, QueueItem: &item}, nil
	}

	userMsg := h.startTurnLocked(rt, sessionID, text, now)
	h.mu.Unlock()

	go h.runPromptLoop(c, rt, text)
	return SendResult{Queued: false, Message: &userMsg}, nil
}

// CancelQueued 从排队队列中撤回一条尚未开始执行的 follow_up / steer 消息。
func (h *Hub) CancelQueued(sessionID, queueID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	rt := h.sessions[sessionID]
	if rt == nil {
		return errors.New("会话不存在")
	}
	idx := slices.IndexFunc(rt.queue, func(q QueuedPrompt) bool { return q.ID == queueID })
	if idx < 0 {
		return errors.New("队列里没有这条待发消息")
	}
	rt.queue = slices.Delete(rt.queue, idx, idx+1)
	h.broadcastLocked(rt, "queue.update", slices.Clone(rt.queue))
	h.broadcastLocked(rt, "session.meta", map[string]any{
		"session":     h.summaryFromRuntimeLocked(rt),
		"used_tokens": rt.usedTokens,
		"max_tokens":  rt.maxTokens,
	})
	return nil
}

func (h *Hub) startTurnLocked(rt *sessionRuntime, sessionID, text string, now time.Time) ChatMessage {
	userMsg := ChatMessage{
		ID:        newShortID("umsg"),
		Role:      "user",
		Text:      text,
		CreatedAt: now,
	}
	rt.msgs = append(rt.msgs, userMsg)
	rt.busy = true
	rt.streaming.Reset()
	rt.thinking.Reset()
	rt.thinkStart = time.Time{}
	rt.thinkEnd = time.Time{}
	rt.liveTools = nil
	rt.updatedAt = now
	if rt.title == "" || rt.title == "新对话" {
		rt.title = truncateRunes(firstNonEmptyLine(text), 28)
	}
	m := h.meta[sessionID]
	m.ID, m.ProjectID, m.Cwd, m.Title, m.Mode, m.Model, m.Deleted, m.UpdatedAt =
		sessionID, rt.projectID, rt.cwd, rt.title, rt.mode, rt.model, false, now
	h.meta[sessionID] = m
	h.saveMetaLocked()

	h.broadcastLocked(rt, "message", userMsg)
	h.broadcastLocked(rt, "status", map[string]any{"busy": true, "title": rt.title})
	return userMsg
}

func (h *Hub) runPromptLoop(c *acpClient, rt *sessionRuntime, text string) {
	curText := text
	curClient := c
	for curText != "" {
		h.executeOneTurn(curClient, rt, curText)

		h.mu.Lock()
		if h.closed || h.sessions[rt.id] != rt || len(rt.queue) == 0 {
			rt.busy = false
			h.broadcastLocked(rt, "status", map[string]any{"busy": false, "title": rt.title})
			h.mu.Unlock()
			return
		}
		next := rt.queue[0]
		rt.queue = rt.queue[1:]
		h.broadcastLocked(rt, "queue.update", slices.Clone(rt.queue))
		needEnsure := curClient == nil || !curClient.alive() || !rt.loaded
		h.mu.Unlock()

		if needEnsure {
			var err error
			curClient, rt, err = h.ensureLoaded(context.Background(), rt.id, rt.projectID)
			if err != nil {
				h.mu.Lock()
				rt.msgs = append(rt.msgs, ChatMessage{
					ID:        newShortID("amsg"),
					Role:      "assistant",
					Error:     err.Error(),
					CreatedAt: time.Now(),
				})
				h.broadcastLocked(rt, "message", rt.msgs[len(rt.msgs)-1])
				rt.busy = false
				h.broadcastLocked(rt, "status", map[string]any{"busy": false, "title": rt.title})
				h.mu.Unlock()
				return
			}
		}

		h.mu.Lock()
		h.startTurnLocked(rt, rt.id, next.Text, time.Now())
		curText = next.Text
		h.mu.Unlock()
	}
}

func (h *Hub) executeOneTurn(c *acpClient, rt *sessionRuntime, text string) {
	var promptRes struct {
		StopReason string `json:"stopReason"`
	}
	err := c.call(context.Background(), "session/prompt", map[string]any{
		"sessionId": rt.id,
		"prompt": []map[string]any{
			{"type": "text", "text": text},
		},
	}, &promptRes)

	h.mu.Lock()
	defer h.mu.Unlock()
	// 清理任何未决的权限请求
	for _, p := range rt.perms {
		if p.req.Status == "pending" {
			p.req.Status = "cancelled"
			select {
			case p.ch <- "":
			default:
			}
			h.broadcastLocked(rt, "permission.upsert", p.req)
		}
	}

	replyText := strings.TrimSpace(rt.streaming.String())
	thinkText := strings.TrimSpace(rt.thinking.String())
	var thinkMs int64
	if thinkText != "" && !rt.thinkStart.IsZero() {
		end := rt.thinkEnd
		if end.IsZero() {
			end = time.Now()
		}
		thinkMs = end.Sub(rt.thinkStart).Milliseconds()
	}
	finalizeTools(rt.liveTools)
	var errMsg string
	if err != nil && promptRes.StopReason != "cancelled" {
		errMsg = err.Error()
	}
	if replyText != "" || thinkText != "" || len(rt.liveTools) > 0 || errMsg != "" {
		asst := ChatMessage{
			ID:         newShortID("amsg"),
			Role:       "assistant",
			Text:       replyText,
			Thinking:   truncateStr(thinkText, 24000),
			ThinkingMs: thinkMs,
			Tools:      slices.Clone(rt.liveTools),
			Error:      errMsg,
			CreatedAt:  time.Now(),
		}
		rt.msgs = append(rt.msgs, asst)
		h.broadcastLocked(rt, "message", asst)
	}
	hasMore := len(rt.queue) > 0 && !h.closed && h.sessions[rt.id] == rt
	rt.streaming.Reset()
	rt.thinking.Reset()
	rt.thinkStart = time.Time{}
	rt.thinkEnd = time.Time{}
	rt.liveTools = nil
	rt.updatedAt = time.Now()
	if !hasMore {
		rt.busy = false
	}
	if m, ok := h.meta[rt.id]; ok {
		m.UpdatedAt = rt.updatedAt
		m.Title = rt.title
		m.Mode = rt.mode
		m.Model = rt.model
		h.meta[rt.id] = m
		h.saveMetaLocked()
	}
	h.broadcastLocked(rt, "turn.done", map[string]any{
		"busy":        hasMore,
		"stop_reason": promptRes.StopReason,
		"error":       errMsg,
	})
}

func (h *Hub) StopPrompt(sessionID string) error {
	h.mu.Lock()
	c := h.client
	rt := h.sessions[sessionID]
	if rt != nil {
		if len(rt.queue) > 0 {
			rt.queue = nil
			h.broadcastLocked(rt, "queue.update", []QueuedPrompt{})
		}
		for _, p := range rt.perms {
			if p.req.Status == "pending" {
				p.req.Status = "cancelled"
				select {
				case p.ch <- "":
				default:
				}
				h.broadcastLocked(rt, "permission.upsert", p.req)
			}
		}
	}
	h.mu.Unlock()
	if c != nil && c.alive() {
		return c.notify("session/cancel", map[string]any{"sessionId": sessionID})
	}
	return nil
}

func (h *Hub) DecidePermission(sessionID, permID, optionID string) error {
	optionID = strings.TrimSpace(optionID)
	if optionID == "" {
		return errors.New("请选择一个权限选项")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rt := h.sessions[sessionID]
	if rt == nil {
		return errors.New("会话不存在")
	}
	for _, p := range rt.perms {
		if p.req.ID == permID {
			if p.req.Status != "pending" {
				return errors.New("该确认请求已处理")
			}
			valid := false
			for _, opt := range p.req.Options {
				if opt.OptionID == optionID {
					valid = true
					break
				}
			}
			if !valid {
				return errors.New("无效的权限选项")
			}
			p.req.Status = "decided"
			p.req.ChosenID = optionID
			select {
			case p.ch <- optionID:
			default:
			}
			h.broadcastLocked(rt, "permission.upsert", p.req)
			return nil
		}
	}
	return errors.New("未找到该确认请求")
}

// Subscribe 打开某个对话的实时事件流，首先返回完整快照，随后推送增量事件。
func (h *Hub) Subscribe(ctx context.Context, sessionID, projectID string) (Snapshot, <-chan StreamEvent, func(), error) {
	_, rt, err := h.ensureLoaded(ctx, sessionID, projectID)
	if err != nil {
		return Snapshot{}, nil, nil, err
	}
	ch := make(chan StreamEvent, 512)
	h.mu.Lock()
	rt.subs[ch] = struct{}{}
	snap := h.snapshotLocked(rt)
	h.mu.Unlock()

	unsub := func() {
		h.mu.Lock()
		if _, ok := rt.subs[ch]; ok {
			delete(rt.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return snap, ch, unsub, nil
}

func (h *Hub) snapshotLocked(rt *sessionRuntime) Snapshot {
	perms := make([]PermissionReq, 0, len(rt.perms))
	for _, p := range rt.perms {
		perms = append(perms, p.req)
	}
	var tsStart, tsEnd int64
	if !rt.thinkStart.IsZero() {
		tsStart = rt.thinkStart.UnixMilli()
	}
	if !rt.thinkEnd.IsZero() {
		tsEnd = rt.thinkEnd.UnixMilli()
	}
	return Snapshot{
		Session:     h.summaryFromRuntimeLocked(rt),
		Messages:    slices.Clone(rt.msgs),
		Permissions: perms,
		Queue:       slices.Clone(rt.queue),
		Streaming:   rt.streaming.String(),
		Thinking:    rt.thinking.String(),
		ThinkStart:  tsStart,
		ThinkEnd:    tsEnd,
		LiveTools:   slices.Clone(rt.liveTools),
		UsedTokens:  rt.usedTokens,
		MaxTokens:   rt.maxTokens,
		Modes:       slices.Clone(h.modes),
		Models:      slices.Clone(h.models),
		Commands:    slices.Clone(h.commands),
	}
}

func (h *Hub) broadcastLocked(rt *sessionRuntime, kind string, data any) {
	rt.seq++
	ev := StreamEvent{Seq: rt.seq, Kind: kind, Time: time.Now(), Data: data}
	for ch := range rt.subs {
		select {
		case ch <- ev:
		default:
			// 客户端太慢时断开让它自动重连拿最新 snapshot
			delete(rt.subs, ch)
			close(ch)
		}
	}
}

// ---------- 处理 agy acp 推来的通知与权限请求 ----------

type acpWireUpdate struct {
	SessionUpdate     string            `json:"sessionUpdate"`
	Content           json.RawMessage   `json:"content"`
	Delta             string            `json:"delta"`
	Text              string            `json:"text"`
	ToolCallID        string            `json:"toolCallId"`
	Name              string            `json:"name"`
	Title             string            `json:"title"`
	Kind              string            `json:"kind"`
	Status            string            `json:"status"`
	Args              json.RawMessage   `json:"args"`
	RawInput          json.RawMessage   `json:"rawInput"`
	RawOutput         json.RawMessage   `json:"rawOutput"`
	Locations         []acpToolLocation `json:"locations"`
	ModeID            string            `json:"modeId"`
	ConfigOptions     []acpConfigOption `json:"configOptions"`
	AvailableCommands []acpCommandWire  `json:"availableCommands"`
	Used              int64             `json:"used"`
	Size              int64             `json:"size"`
	Usage             *struct {
		Used int64 `json:"used"`
		Size int64 `json:"size"`
	} `json:"usage"`
}

type acpToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

func (h *Hub) handleUpdate(sessionID string, raw json.RawMessage) {
	var u acpWireUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	text := extractTextChunk(u.Content, u.Delta, u.Text)
	if u.Usage != nil {
		if u.Used == 0 && u.Usage.Used > 0 {
			u.Used = u.Usage.Used
		}
		if u.Size == 0 && u.Usage.Size > 0 {
			u.Size = u.Usage.Size
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	rt := h.sessions[sessionID]
	if rt == nil {
		return
	}

	if rt.replaying {
		h.applyReplayUpdateLocked(rt, u, text)
		return
	}

	switch u.SessionUpdate {
	case "agent_thought_chunk":
		if text == "" {
			return
		}
		if rt.thinkStart.IsZero() {
			rt.thinkStart = time.Now()
		}
		rt.thinkEnd = time.Time{}
		rt.thinking.WriteString(text)
		h.broadcastLocked(rt, "thinking.delta", map[string]string{"text": text})

	case "agent_message_chunk":
		if text == "" {
			return
		}
		if !rt.thinkStart.IsZero() && rt.thinkEnd.IsZero() {
			rt.thinkEnd = time.Now()
		}
		rt.streaming.WriteString(text)
		h.broadcastLocked(rt, "text.delta", map[string]string{"text": text})

	case "tool_call", "tool_call_update":
		if !rt.thinkStart.IsZero() && rt.thinkEnd.IsZero() {
			rt.thinkEnd = time.Now()
		}
		tc := upsertToolCall(&rt.liveTools, u)
		h.broadcastLocked(rt, "tool.upsert", tc)

	case "current_mode_update":
		if u.ModeID != "" {
			rt.mode = u.ModeID
			h.broadcastLocked(rt, "session.meta", map[string]any{
				"session":     h.summaryFromRuntimeLocked(rt),
				"used_tokens": rt.usedTokens,
				"max_tokens":  rt.maxTokens,
			})
		}

	case "config_option_update", "config_options_update":
		if len(u.ConfigOptions) > 0 {
			h.captureCapabilitiesLocked(acpSessionMetaResp{ConfigOptions: u.ConfigOptions})
			for _, cfg := range u.ConfigOptions {
				cat := strings.ToLower(cfg.Category)
				if cat == "" {
					cat = strings.ToLower(cfg.ID)
				}
				if (cat == "mode" || cat == "permission_mode") && cfg.CurrentValue != "" {
					rt.mode = cfg.CurrentValue
				} else if cat == "model" && cfg.CurrentValue != "" {
					rt.model = cfg.CurrentValue
				}
			}
			h.broadcastLocked(rt, "session.meta", map[string]any{
				"session":     h.summaryFromRuntimeLocked(rt),
				"used_tokens": rt.usedTokens,
				"max_tokens":  rt.maxTokens,
			})
		}

	case "available_commands_update":
		cmds := toCommandOptions(u.AvailableCommands)
		h.commands = cmds
		h.broadcastLocked(rt, "commands", cmds)

	case "session_info_update":
		if t := strings.TrimSpace(u.Title); t != "" {
			rt.title = t
			if m, ok := h.meta[rt.id]; ok {
				m.Title = t
				h.meta[rt.id] = m
				h.saveMetaLocked()
			}
			h.broadcastLocked(rt, "session.meta", map[string]any{
				"session":     h.summaryFromRuntimeLocked(rt),
				"used_tokens": rt.usedTokens,
				"max_tokens":  rt.maxTokens,
			})
		}

	case "usage_update":
		if u.Used > 0 {
			rt.usedTokens = u.Used
		}
		if u.Size > 0 {
			rt.maxTokens = u.Size
		}
		h.broadcastLocked(rt, "session.meta", map[string]any{
			"session":     h.summaryFromRuntimeLocked(rt),
			"used_tokens": rt.usedTokens,
			"max_tokens":  rt.maxTokens,
		})
	}
}

func (h *Hub) applyReplayUpdateLocked(rt *sessionRuntime, u acpWireUpdate, text string) {
	switch u.SessionUpdate {
	case "user_message_chunk":
		// 遇到新一条用户消息前，先把上一轮助理消息落入列表
		if rt.replayAsst.Len() > 0 || rt.replayThk.Len() > 0 || len(rt.replayTls) > 0 {
			h.flushReplayAssistantLocked(rt)
		}
		rt.replayUser.WriteString(text)
	case "agent_thought_chunk":
		h.flushReplayUserLocked(rt)
		rt.replayThk.WriteString(text)
	case "agent_message_chunk":
		h.flushReplayUserLocked(rt)
		rt.replayAsst.WriteString(text)
	case "tool_call", "tool_call_update":
		h.flushReplayUserLocked(rt)
		upsertToolCall(&rt.replayTls, u)
	case "available_commands_update":
		h.commands = toCommandOptions(u.AvailableCommands)
	case "usage_update":
		if u.Used > 0 {
			rt.usedTokens = u.Used
		}
		if u.Size > 0 {
			rt.maxTokens = u.Size
		}
	}
}

func (h *Hub) flushReplayUserLocked(rt *sessionRuntime) {
	if u := strings.TrimSpace(rt.replayUser.String()); u != "" {
		rt.msgs = append(rt.msgs, ChatMessage{
			ID:        newShortID("umsg"),
			Role:      "user",
			Text:      u,
			CreatedAt: time.Now(),
		})
		if rt.title == "" || rt.title == "新对话" {
			rt.title = truncateRunes(firstNonEmptyLine(u), 28)
		}
	}
	rt.replayUser.Reset()
}

func (h *Hub) flushReplayAssistantLocked(rt *sessionRuntime) {
	a := strings.TrimSpace(rt.replayAsst.String())
	th := strings.TrimSpace(rt.replayThk.String())
	finalizeTools(rt.replayTls)
	if a != "" || th != "" || len(rt.replayTls) > 0 {
		rt.msgs = append(rt.msgs, ChatMessage{
			ID:        newShortID("amsg"),
			Role:      "assistant",
			Text:      a,
			Thinking:  truncateStr(th, 24000),
			Tools:     slices.Clone(rt.replayTls),
			CreatedAt: time.Now(),
		})
	}
	rt.replayAsst.Reset()
	rt.replayThk.Reset()
	rt.replayTls = nil
}

func (h *Hub) flushReplayTurnLocked(rt *sessionRuntime, all bool) {
	h.flushReplayUserLocked(rt)
	if all {
		h.flushReplayAssistantLocked(rt)
	}
}

func finalizeTools(tools []ToolCall) {
	for i := range tools {
		if tools[i].Status == "" || tools[i].Status == "in_progress" || tools[i].Status == "pending" {
			tools[i].Status = "completed"
		}
	}
}

func upsertToolCall(list *[]ToolCall, u acpWireUpdate) ToolCall {
	id := u.ToolCallID
	if id == "" {
		id = newShortID("tc")
	}
	detail := extractToolDetail(u.Args, u.RawInput, u.Locations)
	output := extractToolOutput(u.RawOutput, u.Content)
	title := strings.TrimSpace(u.Title)
	if title == "" && detail != "" {
		title = truncateRunes(firstNonEmptyLine(detail), 80)
	}
	status := strings.TrimSpace(u.Status)
	if status == "" && output != "" {
		status = "completed"
	}
	if status == "" {
		status = "in_progress"
	}
	for i := range *list {
		if (*list)[i].ID == id {
			if u.Name != "" {
				(*list)[i].Name = u.Name
			}
			if title != "" {
				(*list)[i].Title = title
			}
			if u.Kind != "" {
				(*list)[i].Kind = u.Kind
			}
			if status != "" {
				(*list)[i].Status = status
			}
			if detail != "" {
				(*list)[i].Detail = detail
			}
			if output != "" {
				(*list)[i].Output = output
			}
			return (*list)[i]
		}
	}
	tc := ToolCall{
		ID:     id,
		Name:   u.Name,
		Title:  title,
		Kind:   u.Kind,
		Status: status,
		Detail: detail,
		Output: output,
	}
	*list = append(*list, tc)
	return tc
}

// extractTextChunk 同时兼容单对象 content、内容块数组 content（如 pi-acp）以及 delta/text 兜底字段。
func extractTextChunk(raw json.RawMessage, delta, text string) string {
	if len(raw) > 0 && string(raw) != "null" {
		var single struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &single) == nil && single.Text != "" {
			return single.Text
		}
		var blocks []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &blocks) == nil {
			var sb strings.Builder
			for _, b := range blocks {
				if b.Text != "" {
					sb.WriteString(b.Text)
				} else if b.Content != nil && b.Content.Text != "" {
					sb.WriteString(b.Content.Text)
				}
			}
			if sb.Len() > 0 {
				return sb.String()
			}
		}
	}
	if delta != "" {
		return delta
	}
	return text
}

func extractToolDetail(args, rawInput json.RawMessage, locs []acpToolLocation) string {
	for _, raw := range []json.RawMessage{args, rawInput} {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			for _, k := range []string{"CommandLine", "command", "cmd", "AbsolutePath", "TargetFile", "Path", "path", "Query", "query", "Url", "url", "toolSummary"} {
				if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
					return truncateRunes(strings.TrimSpace(v), 500)
				}
			}
			if pretty, err := json.Marshal(m); err == nil && len(pretty) > 2 {
				return truncateRunes(string(pretty), 500)
			}
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return truncateRunes(strings.TrimSpace(s), 500)
		}
	}
	if len(locs) > 0 {
		var parts []string
		for _, l := range locs {
			if l.Path == "" {
				continue
			}
			if l.Line > 0 {
				parts = append(parts, fmt.Sprintf("%s:%d", l.Path, l.Line))
			} else {
				parts = append(parts, l.Path)
			}
		}
		if len(parts) > 0 {
			return truncateRunes(strings.Join(parts, ", "), 500)
		}
	}
	return ""
}

func extractToolOutput(rawOutput, content json.RawMessage) string {
	if s := extractContentText(content); s != "" {
		return truncateStr(strings.TrimSpace(s), 4000)
	}
	if len(rawOutput) > 0 && string(rawOutput) != "null" {
		var s string
		if json.Unmarshal(rawOutput, &s) == nil && strings.TrimSpace(s) != "" {
			return truncateStr(strings.TrimSpace(s), 4000)
		}
		var m map[string]any
		if json.Unmarshal(rawOutput, &m) == nil {
			for _, k := range []string{"output", "stdout", "text", "content", "error", "message"} {
				if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
					return truncateStr(strings.TrimSpace(v), 4000)
				}
			}
			if pretty, err := json.MarshalIndent(m, "", "  "); err == nil && len(pretty) > 2 {
				return truncateStr(string(pretty), 4000)
			}
		}
	}
	return ""
}

func extractContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if txt := extractTextChunk(raw, "", ""); txt != "" {
		return txt
	}
	// ACP diff/terminal content blocks: [{type:"diff", path, oldText, newText}]
	var diffs []struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		OldText string `json:"oldText"`
		NewText string `json:"newText"`
	}
	if json.Unmarshal(raw, &diffs) == nil {
		var sb strings.Builder
		for _, d := range diffs {
			if d.Type == "diff" && d.Path != "" {
				fmt.Fprintf(&sb, "diff %s (%d -> %d chars)\n", d.Path, len(d.OldText), len(d.NewText))
			}
		}
		return sb.String()
	}
	return ""
}

func (h *Hub) handlePermission(ctx context.Context, req permWireRequest) string {
	title := strings.TrimSpace(req.ToolCall.Title)
	if title == "" {
		title = req.ToolCall.Name
	}
	if title == "" {
		title = "请求执行操作"
	}
	detail := extractPermDetail(req.ToolCall.Args, req.ToolCall.RawInput)
	options := req.Options
	if len(options) == 0 {
		options = []PermOption{
			{OptionID: "allow_once", Name: "允许一次", Kind: "allow_once"},
			{OptionID: "reject_once", Name: "拒绝", Kind: "reject_once"},
		}
	}
	p := &pendingPerm{
		req: PermissionReq{
			ID:        newShortID("perm"),
			SessionID: req.SessionID,
			ToolName:  req.ToolCall.Name,
			Title:     title,
			Kind:      req.ToolCall.Kind,
			Detail:    detail,
			Options:   options,
			Status:    "pending",
			CreatedAt: time.Now(),
		},
		ch: make(chan string, 1),
	}

	h.mu.Lock()
	rt := h.sessions[req.SessionID]
	if rt == nil {
		h.mu.Unlock()
		return ""
	}
	rt.perms = append(rt.perms, p)
	h.broadcastLocked(rt, "permission.upsert", p.req)
	h.mu.Unlock()

	select {
	case chosen := <-p.ch:
		return chosen
	case <-ctx.Done():
		return ""
	}
}

func extractPermDetail(args, rawInput json.RawMessage) string {
	for _, raw := range []json.RawMessage{args, rawInput} {
		if len(raw) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			for _, k := range []string{"CommandLine", "command", "AbsolutePath", "Path", "path"} {
				if v, ok := m[k].(string); ok && v != "" {
					return truncateRunes(v, 400)
				}
			}
		}
	}
	return ""
}

// ---------- 工具函数 ----------

func newShortID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func realOrClean(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func isWithinDir(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func writeAtomicJSON(path string, v any) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agy-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := json.NewEncoder(tmp).Encode(v); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
