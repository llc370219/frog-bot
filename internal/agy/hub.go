package agy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Config 配置 Coding 工作台的数据目录、默认项目根目录以及进程沙箱包装器。
type Config struct {
	DataDir      string                // frog 数据目录（仅保护其中的密钥，不作为 CLI 安装目录）
	ProjectsRoot string                // 新建托管项目文件夹的默认父目录
	WrapCmd      func(*exec.Cmd) error // 可选：Linux Landlock 沙箱包装（阻止读取 DataDir 里的密钥）
}

// Project 是一个给 Coding CLI 工作的项目文件夹（托管目录或宿主机任意已有目录）。
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Managed   bool      `json:"managed"`
	CreatedAt time.Time `json:"created_at"`
}

type ModeOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StatusView 返回本机已安装的各类 Agent CLI 状态与当前选中 CLI 的能力列表。
type StatusView struct {
	Installed    bool            `json:"installed"`
	DefaultAgent string          `json:"default_agent"`
	Agents       []AgentCLIView  `json:"agents"`
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

type CommandOption struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"input_hint,omitempty"`
}

type QueuedPrompt struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Behavior  string    `json:"behavior,omitempty"` // follow_up | steer
	CreatedAt time.Time `json:"created_at"`
}

type SendResult struct {
	Queued    bool          `json:"queued"`
	Behavior  string        `json:"behavior,omitempty"`
	Message   *ChatMessage  `json:"message,omitempty"`
	QueueItem *QueuedPrompt `json:"queue_item,omitempty"`
}

type SessionSummary struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	Cwd                string    `json:"cwd"`
	Agent              string    `json:"agent,omitempty"`
	AgentName          string    `json:"agent_name,omitempty"`
	Title              string    `json:"title"`
	Mode               string    `json:"mode"`
	Model              string    `json:"model,omitempty"`
	Busy               bool      `json:"busy"`
	PendingPermissions int       `json:"pending_permissions"`
	QueuedCount        int       `json:"queued_count"`
	UpdatedAt          time.Time `json:"updated_at"`
}

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

type ToolCall struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Title  string `json:"title,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"` // pending | in_progress | completed | failed
	Detail string `json:"detail,omitempty"`
	Output string `json:"output,omitempty"`
}

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

type StreamEvent struct {
	Seq  int64     `json:"seq"`
	Kind string    `json:"kind"`
	Time time.Time `json:"time"`
	Data any       `json:"data"`
}

var defaultModes = []ModeOption{
	{ID: "auto", Name: "Auto（自动执行）", Description: "自动批准工作区修改与常规命令"},
	{ID: "accept-edits", Name: "Accept Edits（自动改文件）", Description: "工作区内改文件免确认"},
	{ID: "plan", Name: "Plan（先规划）", Description: "先制定方案再修改"},
	{ID: "default", Name: "Default（默认询问）", Description: "按权限策略询问"},
}

type savedSessionMeta struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Cwd       string    `json:"cwd"`
	Agent     string    `json:"agent,omitempty"`
	NativeID  string    `json:"native_id,omitempty"`
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
	id            string
	projectID     string
	cwd           string
	agent         string
	nativeID      string
	title         string
	mode          string
	model         string
	updatedAt     time.Time
	loaded        bool
	replaying     bool
	busy          bool
	turnCancel    context.CancelFunc
	codexTurnID   string
	codexTurnDone chan error
	msgs          []ChatMessage
	perms         []*pendingPerm
	queue         []QueuedPrompt
	streaming     strings.Builder
	thinking      strings.Builder
	thinkStart    time.Time
	thinkEnd      time.Time
	liveTools     []ToolCall
	replayUser    strings.Builder
	replayAsst    strings.Builder
	replayThk     strings.Builder
	replayTls     []ToolCall
	usedTokens    int64
	maxTokens     int64
	seq           int64
	subs          map[chan StreamEvent]struct{}
}

type agentDynamicCaps struct {
	modes    []ModeOption
	models   []ModelOption
	commands []CommandOption
	defMode  string
	defModel string
	version  string
}

// Hub 统一管理本机多款 Coding Agent CLI（agy / Claude Code / Codex / Pi / Gemini / OpenCode）的项目、会话与实时事件流。
type Hub struct {
	cfg      Config
	dataDir  string
	projRoot string

	mu          sync.Mutex
	clientMu    sync.Mutex
	acpClients  map[string]*acpClient
	codexCli    *codexClient
	claudeRuns  map[string]*claudeSessionRunner
	piRuns      map[string]*piSessionRunner
	agentCaps   map[string]*agentDynamicCaps
	defAgent    string
	projects    []Project
	meta        map[string]savedSessionMeta
	nativeCache map[string]nativeSessionInfo
	sessions    map[string]*sessionRuntime
	closed      bool
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
		cfg:         cfg,
		dataDir:     dataDir,
		projRoot:    projRoot,
		acpClients:  map[string]*acpClient{},
		claudeRuns:  map[string]*claudeSessionRunner{},
		piRuns:      map[string]*piSessionRunner{},
		agentCaps:   map[string]*agentDynamicCaps{},
		meta:        map[string]savedSessionMeta{},
		nativeCache: map[string]nativeSessionInfo{},
		sessions:    map[string]*sessionRuntime{},
	}
	for _, sp := range agentCatalog {
		h.agentCaps[sp.ID] = &agentDynamicCaps{
			modes:    slices.Clone(sp.Modes),
			models:   slices.Clone(sp.Models),
			defMode:  sp.DefaultMode,
			defModel: sp.DefaultModel,
		}
	}
	_ = h.loadProjects()
	_ = h.loadMeta()
	_ = h.loadPrefs()
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
	acps := make([]*acpClient, 0, len(h.acpClients))
	for k, c := range h.acpClients {
		acps = append(acps, c)
		delete(h.acpClients, k)
	}
	codex := h.codexCli
	h.codexCli = nil
	claudes := make([]*claudeSessionRunner, 0, len(h.claudeRuns))
	for k, r := range h.claudeRuns {
		claudes = append(claudes, r)
		delete(h.claudeRuns, k)
	}
	pis := make([]*piSessionRunner, 0, len(h.piRuns))
	for k, r := range h.piRuns {
		pis = append(pis, r)
		delete(h.piRuns, k)
	}
	for _, rt := range h.sessions {
		if rt.turnCancel != nil {
			rt.turnCancel()
		}
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

	for _, c := range acps {
		c.Close()
	}
	if codex != nil {
		codex.Close()
	}
	for _, r := range claudes {
		r.Close()
	}
	for _, r := range pis {
		r.Close()
	}
}

// ---------- 偏好与历史持久化 ----------

func (h *Hub) prefsPath() string   { return filepath.Join(h.dataDir, "agy-prefs.json") }
func (h *Hub) projectsPath() string { return filepath.Join(h.dataDir, "agy-projects.json") }
func (h *Hub) metaPath() string     { return filepath.Join(h.dataDir, "agy-sessions.json") }

func (h *Hub) historyFilePath(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(h.dataDir, "agy-history", hex.EncodeToString(sum[:12])+".json")
}

func (h *Hub) saveHistoryLocked(rt *sessionRuntime) {
	if len(rt.msgs) == 0 {
		return
	}
	p := h.historyFilePath(rt.id)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	msgs := rt.msgs
	if len(msgs) > 200 {
		msgs = msgs[len(msgs)-200:]
	}
	_ = writeAtomicJSON(p, msgs)
}

func (h *Hub) loadHistory(sessionID string) []ChatMessage {
	raw, err := os.ReadFile(h.historyFilePath(sessionID))
	if err != nil {
		return nil
	}
	var msgs []ChatMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return nil
	}
	return msgs
}

func (h *Hub) historyModTime(sessionID string) time.Time {
	fi, err := os.Stat(h.historyFilePath(sessionID))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

func (h *Hub) loadPrefs() error {
	raw, err := os.ReadFile(h.prefsPath())
	if err != nil {
		return err
	}
	var p struct {
		DefaultAgent string `json:"default_agent"`
	}
	if err := json.Unmarshal(raw, &p); err == nil && p.DefaultAgent != "" {
		if _, ok := findAgentSpec(p.DefaultAgent); ok {
			h.defAgent = p.DefaultAgent
		}
	}
	return nil
}

func (h *Hub) savePrefsLocked() {
	_ = writeAtomicJSON(h.prefsPath(), map[string]string{"default_agent": h.defAgent})
}

func (h *Hub) resolveActiveAgentLocked(discovered []AgentCLIView) string {
	if h.defAgent != "" {
		for _, a := range discovered {
			if a.ID == h.defAgent && a.Installed {
				return a.ID
			}
		}
	}
	for _, a := range discovered {
		if a.Installed {
			return a.ID
		}
	}
	if h.defAgent != "" {
		return h.defAgent
	}
	return "agy"
}

func agentDisplayName(id string) string {
	if sp, ok := findAgentSpec(id); ok {
		return sp.Name
	}
	return id
}

// ---------- 项目文件夹管理 ----------

var folderSlugRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._-]{0,63}$`)

func (h *Hub) ListProjects() []Project {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.projects) == 0 {
		return []Project{}
	}
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
			return Project{}, errors.New("不能将 frog 自身的数据目录设为项目文件夹")
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
	targetKey := pathMatchKey(target)
	for _, existing := range h.projects {
		if pathMatchKey(existing.Path) == targetKey {
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
	var acpSessIDs []struct {
		agent string
		id    string
	}
	projKey := pathMatchKey(p.Path)
	for sid, rt := range h.sessions {
		if rt.projectID == id || (projKey != "" && pathMatchKey(rt.cwd) == projKey) {
			acpSessIDs = append(acpSessIDs, struct {
				agent string
				id    string
			}{rt.agent, sid})
		}
	}
	h.projects = slices.Delete(slices.Clone(h.projects), idx, idx+1)
	if err := h.saveProjectsLocked(); err != nil {
		h.mu.Unlock()
		return err
	}
	h.mu.Unlock()

	for _, s := range acpSessIDs {
		_ = h.DeleteSession(context.Background(), s.id)
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
		if m.Agent == "" {
			m.Agent = "agy"
		}
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

// ---------- 各协议客户端初始化 ----------

func (h *Hub) ensureACPClient(agentID string) (*acpClient, error) {
	spec, ok := findAgentSpec(agentID)
	if !ok || spec.Protocol != "acp" {
		return nil, fmt.Errorf("不支持的 ACP CLI：%s", agentID)
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("服务正在关闭")
	}
	if c := h.acpClients[agentID]; c != nil && c.alive() {
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
	if c := h.acpClients[agentID]; c != nil && c.alive() {
		h.mu.Unlock()
		return c, nil
	}
	h.mu.Unlock()

	exe, err := ResolveAgentBinary(agentID)
	if err != nil {
		return nil, err
	}
	var created *acpClient
	created, err = startACPClient(
		exe,
		spec.ACPArgs,
		h.cfg.WrapCmd,
		func(sid string, raw json.RawMessage) { h.handleACPUpdate(agentID, sid, raw) },
		h.handlePermission,
		func(_ error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.acpClients[agentID] == created {
				delete(h.acpClients, agentID)
			}
			for _, rt := range h.sessions {
				if rt.agent == agentID {
					rt.loaded = false
				}
			}
		},
	)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		created.Close()
		return nil, errors.New("服务正在关闭")
	}
	h.acpClients[agentID] = created
	if cap := h.agentCaps[agentID]; cap != nil && created.agentVer != "" {
		cap.version = created.agentVer
	}
	h.mu.Unlock()
	return created, nil
}

func (h *Hub) ensureCodexClient() (*codexClient, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("服务正在关闭")
	}
	if h.codexCli != nil && h.codexCli.alive() {
		c := h.codexCli
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
	if h.codexCli != nil && h.codexCli.alive() {
		c := h.codexCli
		h.mu.Unlock()
		return c, nil
	}
	h.mu.Unlock()

	exe, err := ResolveAgentBinary("codex")
	if err != nil {
		return nil, err
	}
	var created *codexClient
	created, err = startCodexClient(
		exe,
		h.cfg.WrapCmd,
		func(threadID, kind, text string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if rt := h.findByNativeIDLocked("codex", threadID); rt != nil {
				h.appendDeltaLocked(rt, kind, text)
			}
		},
		func(threadID string, tc ToolCall) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if rt := h.findByNativeIDLocked("codex", threadID); rt != nil {
				h.mergeToolCallLocked(rt, tc)
			}
		},
		func(ctx context.Context, threadID string, req permWireRequest) string {
			h.mu.Lock()
			rt := h.findByNativeIDLocked("codex", threadID)
			sid := ""
			if rt != nil {
				sid = rt.id
			}
			h.mu.Unlock()
			if sid == "" {
				return "accept"
			}
			req.SessionID = sid
			return h.handlePermission(ctx, req)
		},
		func(threadID string, terr error) {
			h.mu.Lock()
			var ch chan error
			if rt := h.findByNativeIDLocked("codex", threadID); rt != nil {
				ch = rt.codexTurnDone
				rt.codexTurnDone = nil
			}
			h.mu.Unlock()
			if ch != nil {
				ch <- terr
			}
		},
		func(derr error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.codexCli == created {
				h.codexCli = nil
			}
			for _, rt := range h.sessions {
				if rt.agent == "codex" {
					rt.loaded = false
					if rt.codexTurnDone != nil {
						rt.codexTurnDone <- derr
						rt.codexTurnDone = nil
					}
				}
			}
		},
	)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		created.Close()
		return nil, errors.New("服务正在关闭")
	}
	h.codexCli = created
	h.mu.Unlock()
	return created, nil
}

func (h *Hub) findByNativeIDLocked(agentID, nativeID string) *sessionRuntime {
	for _, rt := range h.sessions {
		if rt.agent == agentID && (rt.nativeID == nativeID || rt.id == nativeID) {
			return rt
		}
	}
	return nil
}

func (h *Hub) appendDeltaLocked(rt *sessionRuntime, kind, text string) {
	if text == "" {
		return
	}
	if kind == "thinking" {
		if rt.thinkStart.IsZero() {
			rt.thinkStart = time.Now()
		}
		rt.thinkEnd = time.Time{}
		rt.thinking.WriteString(text)
		h.broadcastLocked(rt, "thinking.delta", map[string]string{"text": text})
		return
	}
	if !rt.thinkStart.IsZero() && rt.thinkEnd.IsZero() {
		rt.thinkEnd = time.Now()
	}
	rt.streaming.WriteString(text)
	h.broadcastLocked(rt, "text.delta", map[string]string{"text": text})
}

func (h *Hub) mergeToolCallLocked(rt *sessionRuntime, tc ToolCall) {
	if !rt.thinkStart.IsZero() && rt.thinkEnd.IsZero() {
		rt.thinkEnd = time.Now()
	}
	if tc.ID == "" {
		tc.ID = newShortID("tc")
	}
	for i := range rt.liveTools {
		if rt.liveTools[i].ID == tc.ID {
			if tc.Name != "" {
				rt.liveTools[i].Name = tc.Name
			}
			if tc.Title != "" {
				rt.liveTools[i].Title = tc.Title
			}
			if tc.Status != "" {
				rt.liveTools[i].Status = tc.Status
			}
			if tc.Detail != "" {
				rt.liveTools[i].Detail = tc.Detail
			}
			if tc.Output != "" {
				rt.liveTools[i].Output = tc.Output
			}
			h.broadcastLocked(rt, "tool.upsert", rt.liveTools[i])
			return
		}
	}
	if tc.Status == "" {
		tc.Status = "in_progress"
	}
	rt.liveTools = append(rt.liveTools, tc)
	h.broadcastLocked(rt, "tool.upsert", tc)
}

// ---------- 状态与默认 Agent 切换 ----------

func (h *Hub) SetDefaultAgent(ctx context.Context, agentID string) (StatusView, error) {
	agentID = strings.ToLower(strings.TrimSpace(agentID))
	if _, ok := findAgentSpec(agentID); !ok {
		return StatusView{}, errors.New("不支持的 Agent CLI")
	}
	if _, err := ResolveAgentBinary(agentID); err != nil {
		return StatusView{}, err
	}
	h.mu.Lock()
	h.defAgent = agentID
	h.savePrefsLocked()
	h.mu.Unlock()
	return h.Status(ctx), nil
}

func (h *Hub) Status(ctx context.Context) StatusView {
	discovered := DiscoverAgents()

	h.mu.Lock()
	activeID := h.resolveActiveAgentLocked(discovered)
	for i := range discovered {
		if cap := h.agentCaps[discovered[i].ID]; cap != nil {
			if len(cap.modes) > 0 {
				discovered[i].Modes = slices.Clone(cap.modes)
			}
			if len(cap.models) > 0 {
				discovered[i].Models = slices.Clone(cap.models)
			}
			if cap.defMode != "" {
				discovered[i].DefaultMode = cap.defMode
			}
			if cap.defModel != "" {
				discovered[i].DefaultModel = cap.defModel
			}
			if cap.version != "" {
				discovered[i].Version = cap.version
			}
		}
	}
	root := h.projRoot
	h.mu.Unlock()

	var chosen AgentCLIView
	anyInstalled := false
	for _, a := range discovered {
		if a.Installed {
			anyInstalled = true
		}
		if a.ID == activeID {
			chosen = a
		}
	}

	// 若当前激活的是 ACP 类 CLI（如 agy），顺带预热连接以拉取版本号
	if chosen.Installed {
		if sp, ok := findAgentSpec(chosen.ID); ok && sp.Protocol == "acp" {
			if c, err := h.ensureACPClient(chosen.ID); err == nil && c.agentVer != "" {
				chosen.Version = c.agentVer
			}
		}
	}

	h.mu.Lock()
	var cmds []CommandOption
	if cap := h.agentCaps[activeID]; cap != nil {
		cmds = slices.Clone(cap.commands)
		if len(cap.modes) > 0 {
			chosen.Modes = slices.Clone(cap.modes)
		}
		if len(cap.models) > 0 {
			chosen.Models = slices.Clone(cap.models)
		}
	}
	h.mu.Unlock()

	modes := chosen.Modes
	if modes == nil {
		modes = []ModeOption{}
	}
	models := chosen.Models
	if models == nil {
		models = []ModelOption{}
	}
	if discovered == nil {
		discovered = []AgentCLIView{}
	}
	view := StatusView{
		Installed:    anyInstalled,
		DefaultAgent: activeID,
		Agents:       discovered,
		Path:         chosen.Path,
		AgentVersion: chosen.Version,
		ProjectsRoot: root,
		Modes:        modes,
		Models:       models,
		Commands:     cmds,
		DefaultMode:  chosen.DefaultMode,
		DefaultModel: chosen.DefaultModel,
	}
	if !anyInstalled {
		view.Error = "本机未检测到已安装的 Coding CLI（支持 Antigravity / Claude Code / Codex / Pi / Gemini / OpenCode）"
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

func (h *Hub) captureCapabilitiesLocked(agentID string, resp acpSessionMetaResp) {
	cap := h.agentCaps[agentID]
	if cap == nil {
		cap = &agentDynamicCaps{}
		h.agentCaps[agentID] = cap
	}
	if len(resp.Modes.AvailableModes) > 0 {
		cap.modes = resp.Modes.AvailableModes
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
			cap.models = models
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
					cap.modes = modes
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
					cap.models = models
				}
			}
			if cfg.CurrentValue != "" && cap.defModel == "" {
				cap.defModel = cfg.CurrentValue
			}
		}
	}
	if resp.Models.CurrentModelID != "" && cap.defModel == "" {
		cap.defModel = resp.Models.CurrentModelID
	}
	if len(resp.AvailableCommands) > 0 {
		cap.commands = toCommandOptions(resp.AvailableCommands)
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

type projMatchEntry struct {
	id  string
	key string
}

func resolveProjectForCwd(cwdKey string, projs []projMatchEntry) string {
	if cwdKey == "" {
		return ""
	}
	for _, p := range projs {
		if p.key != "" && p.key == cwdKey {
			return p.id
		}
	}
	bestID := ""
	bestLen := 0
	for _, p := range projs {
		if p.key == "" {
			continue
		}
		prefix := strings.TrimSuffix(p.key, "/") + "/"
		if strings.HasPrefix(cwdKey, prefix) && len(p.key) > bestLen {
			bestID = p.id
			bestLen = len(p.key)
		}
	}
	return bestID
}

func (h *Hub) listACPNativeSessions(ctx context.Context, agentID, fallbackCwd string) []nativeSessionInfo {
	if _, err := ResolveAgentBinary(agentID); err != nil {
		return nil
	}
	c, err := h.ensureACPClient(agentID)
	if err != nil {
		return nil
	}
	type wireItem struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
		Title     string `json:"title"`
		UpdatedAt string `json:"updatedAt"`
	}
	type listPage struct {
		Sessions   []wireItem `json:"sessions"`
		NextCursor string     `json:"nextCursor"`
	}

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// 先不带 cwd 过滤拉取全量原生会话，避免 Windows 盘符大小写 / 斜杠 / URI / 子目录差异被远端过滤掉；
	// 仅在不支持无参调用时回退带 cwd。
	useCwdFilter := false
	var first listPage
	if err := c.call(callCtx, "session/list", map[string]any{}, &first); err != nil {
		if fallbackCwd == "" {
			return nil
		}
		if err2 := c.call(callCtx, "session/list", map[string]any{"cwd": fallbackCwd}, &first); err2 != nil {
			return nil
		}
		useCwdFilter = true
	}

	rawItems := append([]wireItem(nil), first.Sessions...)
	cursor := strings.TrimSpace(first.NextCursor)
	seenCursor := map[string]bool{}
	for page := 1; cursor != "" && page < 10 && !seenCursor[cursor]; page++ {
		seenCursor[cursor] = true
		params := map[string]any{"cursor": cursor}
		if useCwdFilter && fallbackCwd != "" {
			params["cwd"] = fallbackCwd
		}
		var next listPage
		if c.call(callCtx, "session/list", params, &next) != nil || len(next.Sessions) == 0 {
			break
		}
		rawItems = append(rawItems, next.Sessions...)
		cursor = strings.TrimSpace(next.NextCursor)
	}

	out := make([]nativeSessionInfo, 0, len(rawItems))
	for _, item := range rawItems {
		if item.SessionID == "" {
			continue
		}
		updated := parseFlexibleTime(item.UpdatedAt)
		if updated.IsZero() {
			updated = time.Now()
		}
		out = append(out, nativeSessionInfo{
			ID:        item.SessionID,
			Agent:     agentID,
			NativeID:  item.SessionID,
			Cwd:       cleanNativeCwd(item.Cwd),
			RawCwd:    item.Cwd,
			Title:     strings.TrimSpace(item.Title),
			UpdatedAt: updated,
		})
	}
	return out
}

func (h *Hub) ListSessions(ctx context.Context, projectID string) ([]SessionSummary, error) {
	projectID = strings.TrimSpace(projectID)
	var proj *Project
	if projectID != "" && !strings.EqualFold(projectID, "all") {
		p, ok := h.FindProject(projectID)
		if !ok {
			return nil, errors.New("项目不存在")
		}
		proj = &p
	}

	cwd := h.projRoot
	if proj != nil {
		cwd = proj.Path
	}

	// 并行或依次收集各原生 CLI 的历史会话：Antigravity (ACP)、Claude Code (~/.claude/projects)、Codex (~/.codex/sessions)
	agyNative := h.listACPNativeSessions(ctx, "agy", cwd)
	claudeNative := discoverNativeClaudeSessions()
	codexNative := discoverNativeCodexSessions()

	allNative := make([]nativeSessionInfo, 0, len(agyNative)+len(claudeNative)+len(codexNative))
	allNative = append(allNative, agyNative...)
	allNative = append(allNative, claudeNative...)
	allNative = append(allNative, codexNative...)

	h.mu.Lock()
	defer h.mu.Unlock()
	projKeys := make([]projMatchEntry, 0, len(h.projects))
	for _, p := range h.projects {
		projKeys = append(projKeys, projMatchEntry{id: p.ID, key: pathMatchKey(p.Path)})
	}
	wantPath := ""
	if proj != nil {
		wantPath = pathMatchKey(proj.Path)
	}

	// 将通过 Frog Bot 创建、随后绑定到原生 CLI sessionId/threadId 的记录映射回已有 ID，防止重复列出
	metaByNative := map[string]string{}
	for id, m := range h.meta {
		if m.NativeID != "" && m.Agent != "" {
			metaByNative[m.Agent+":"+m.NativeID] = id
		}
	}

	matchesProject := func(cwdKey, metaProjID string) (bool, string) {
		pid := resolveProjectForCwd(cwdKey, projKeys)
		if pid == "" {
			pid = metaProjID
		}
		if proj == nil {
			return true, pid
		}
		if cwdKey != "" {
			if cwdKey == wantPath || pid == proj.ID {
				return true, proj.ID
			}
			return false, ""
		}
		if metaProjID == proj.ID {
			return true, proj.ID
		}
		return false, ""
	}

	seen := map[string]bool{}
	out := make([]SessionSummary, 0)
	for _, item := range allNative {
		if item.ID == "" {
			continue
		}
		sid := item.ID
		if item.NativeID != "" {
			if mapped, ok := metaByNative[item.Agent+":"+item.NativeID]; ok {
				sid = mapped
			}
		}
		cached := item
		cached.ID = sid
		h.nativeCache[sid] = cached

		if seen[sid] {
			continue
		}
		m, hasMeta := h.meta[sid]
		if hasMeta && m.Deleted {
			continue
		}
		cwdKey := pathMatchKey(item.Cwd)
		matched, pid := matchesProject(cwdKey, m.ProjectID)
		if !matched {
			continue
		}
		seen[sid] = true

		ag := item.Agent
		if ag == "" {
			ag = "agy"
		}
		var defMode, defModel string
		if cap := h.agentCaps[ag]; cap != nil {
			defMode = cap.defMode
			defModel = cap.defModel
		}
		model := item.Model
		if model == "" {
			model = defModel
		}
		updated := item.UpdatedAt
		if updated.IsZero() {
			updated = time.Now()
		}
		s := SessionSummary{
			ID:        sid,
			ProjectID: pid,
			Cwd:       item.Cwd,
			Agent:     ag,
			AgentName: agentDisplayName(ag),
			Title:     strings.TrimSpace(item.Title),
			Mode:      defMode,
			Model:     model,
			UpdatedAt: updated,
		}
		if hasMeta {
			if m.Title != "" && m.Title != "新对话" {
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
		if rt := h.sessions[sid]; rt != nil {
			s = h.summaryFromRuntimeLocked(rt)
		}
		if s.Title == "" {
			s.Title = "新对话"
		}
		out = append(out, s)
	}

	for id, m := range h.meta {
		if m.Deleted || seen[id] {
			continue
		}
		cwdKey := pathMatchKey(m.Cwd)
		matched, pid := matchesProject(cwdKey, m.ProjectID)
		if !matched {
			continue
		}
		if rt := h.sessions[id]; rt != nil {
			out = append(out, h.summaryFromRuntimeLocked(rt))
		} else {
			ag := m.Agent
			if ag == "" {
				ag = "agy"
			}
			title := m.Title
			if title == "" {
				title = "新对话"
			}
			mode := m.Mode
			if mode == "" {
				if cap := h.agentCaps[ag]; cap != nil {
					mode = cap.defMode
				}
			}
			out = append(out, SessionSummary{
				ID:        m.ID,
				ProjectID: pid,
				Cwd:       m.Cwd,
				Agent:     ag,
				AgentName: agentDisplayName(ag),
				Title:     title,
				Mode:      mode,
				Model:     m.Model,
				UpdatedAt: m.UpdatedAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(listItemTime(out[j])) })
	return out, nil
}

func listItemTime(s SessionSummary) time.Time { return s.UpdatedAt }

func (h *Hub) summaryFromRuntimeLocked(rt *sessionRuntime) SessionSummary {
	pending := 0
	for _, p := range rt.perms {
		if p.req.Status == "pending" {
			pending++
		}
	}
	ag := rt.agent
	if ag == "" {
		ag = "agy"
	}
	title := rt.title
	if title == "" {
		title = "新对话"
	}
	mode := rt.mode
	if mode == "" {
		if cap := h.agentCaps[ag]; cap != nil {
			mode = cap.defMode
		}
	}
	return SessionSummary{
		ID:                 rt.id,
		ProjectID:          rt.projectID,
		Cwd:                rt.cwd,
		Agent:              ag,
		AgentName:          agentDisplayName(ag),
		Title:              title,
		Mode:               mode,
		Model:              rt.model,
		Busy:               rt.busy,
		PendingPermissions: pending,
		QueuedCount:        len(rt.queue),
		UpdatedAt:          rt.updatedAt,
	}
}

// CreateSession 在指定项目目录下使用本机指定的 Agent CLI 创建新会话。
func (h *Hub) CreateSession(ctx context.Context, projectID, agentID, mode, model string) (Snapshot, error) {
	proj, ok := h.FindProject(projectID)
	if !ok {
		projects := h.ListProjects()
		if len(projects) == 0 {
			return Snapshot{}, errors.New("请先创建或关联一个项目文件夹")
		}
		proj = projects[0]
	}

	discovered := DiscoverAgents()
	h.mu.Lock()
	if strings.TrimSpace(agentID) == "" {
		agentID = h.resolveActiveAgentLocked(discovered)
	} else {
		agentID = strings.ToLower(strings.TrimSpace(agentID))
	}
	h.mu.Unlock()

	spec, ok := findAgentSpec(agentID)
	if !ok {
		return Snapshot{}, fmt.Errorf("不支持的 Agent CLI：%s", agentID)
	}
	if _, err := ResolveAgentBinary(agentID); err != nil {
		return Snapshot{}, err
	}

	wantMode := strings.TrimSpace(mode)
	if wantMode == "" {
		wantMode = spec.DefaultMode
	}
	wantModel := strings.TrimSpace(model)
	if wantModel == "" {
		wantModel = spec.DefaultModel
	}

	var sessionID, nativeID string
	curMode, curModel := wantMode, wantModel

	switch spec.Protocol {
	case "acp":
		c, err := h.ensureACPClient(agentID)
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
			return Snapshot{}, fmt.Errorf("新建 %s 对话失败：%w", spec.Name, err)
		}
		if res.SessionID == "" {
			return Snapshot{}, fmt.Errorf("%s 未返回会话 ID", spec.Name)
		}
		sessionID = res.SessionID
		curMode = res.Modes.CurrentModeID
		if wantMode != "" && wantMode != curMode {
			if c.call(callCtx, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": wantMode}, nil) == nil {
				curMode = wantMode
			}
		}
		if curMode == "" {
			curMode = spec.DefaultMode
		}
		curModel = res.Models.CurrentModelID
		if wantModel != "" && wantModel != curModel {
			if c.call(callCtx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": wantModel}, nil) == nil {
				curModel = wantModel
			}
		}
		h.mu.Lock()
		h.captureCapabilitiesLocked(agentID, res)
		h.mu.Unlock()

	case "codex":
		c, err := h.ensureCodexClient()
		if err != nil {
			return Snapshot{}, err
		}
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		approval, sb := codexModeParams(wantMode)
		params := map[string]any{
			"cwd":            proj.Path,
			"approvalPolicy": approval,
			"sandbox":        sb,
		}
		if wantModel != "" && wantModel != "default" {
			params["model"] = wantModel
		}
		var res struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := c.call(callCtx, "thread/start", params, &res); err != nil {
			return Snapshot{}, fmt.Errorf("启动 Codex 会话失败：%w", err)
		}
		nativeID = res.Thread.ID
		if nativeID == "" {
			return Snapshot{}, errors.New("Codex 未返回 threadId")
		}
		sessionID = "codex_" + nativeID

	case "claude":
		sessionID = newShortID("claude")

	case "pi":
		sessionID = newShortID("pi")
	}

	now := time.Now()
	h.mu.Lock()
	h.defAgent = agentID
	h.savePrefsLocked()
	rt := h.getOrCreateRuntimeLocked(sessionID, proj.ID, proj.Path, agentID)
	rt.loaded = true
	rt.agent = agentID
	rt.nativeID = nativeID
	rt.title = "新对话"
	rt.mode = curMode
	rt.model = curModel
	rt.updatedAt = now
	h.meta[sessionID] = savedSessionMeta{
		ID:        sessionID,
		ProjectID: proj.ID,
		Cwd:       proj.Path,
		Agent:     agentID,
		NativeID:  nativeID,
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

func (h *Hub) getOrCreateRuntimeLocked(id, projectID, cwd, agentID string) *sessionRuntime {
	if strings.EqualFold(strings.TrimSpace(projectID), "all") {
		projectID = ""
	}
	if rt := h.sessions[id]; rt != nil {
		if projectID != "" {
			rt.projectID = projectID
		}
		if cwd != "" {
			rt.cwd = cwd
		}
		if agentID != "" && rt.agent == "" {
			rt.agent = agentID
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
		if agentID == "" {
			agentID = m.Agent
		}
	}
	if ni, ok := h.nativeCache[id]; ok {
		if cwd == "" && ni.Cwd != "" {
			cwd = ni.Cwd
		}
		if agentID == "" && ni.Agent != "" {
			agentID = ni.Agent
		}
	}
	if agentID == "" {
		if strings.HasPrefix(id, "claude_") {
			agentID = "claude"
		} else if strings.HasPrefix(id, "codex_") {
			agentID = "codex"
		} else if strings.HasPrefix(id, "pi_") {
			agentID = "pi"
		} else {
			agentID = "agy"
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
	cap := h.agentCaps[agentID]
	defMode, defModel := "auto", ""
	if cap != nil {
		defMode = cap.defMode
		defModel = cap.defModel
	}
	rt := &sessionRuntime{
		id:        id,
		projectID: projectID,
		cwd:       cwd,
		agent:     agentID,
		mode:      defMode,
		model:     defModel,
		updatedAt: time.Now(),
		subs:      map[chan StreamEvent]struct{}{},
	}
	if ni, ok := h.nativeCache[id]; ok {
		if ni.NativeID != "" {
			rt.nativeID = ni.NativeID
		}
		if ni.Title != "" {
			rt.title = ni.Title
		}
		if ni.Model != "" {
			rt.model = ni.Model
		}
		if !ni.UpdatedAt.IsZero() {
			rt.updatedAt = ni.UpdatedAt
		}
	}
	if m, ok := h.meta[id]; ok {
		if m.NativeID != "" {
			rt.nativeID = m.NativeID
		}
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
	if rt.nativeID == "" {
		if agentID == "codex" && strings.HasPrefix(id, "codex_") {
			rt.nativeID = strings.TrimPrefix(id, "codex_")
		} else if agentID == "claude" && strings.HasPrefix(id, "claude_") {
			if cand := strings.TrimPrefix(id, "claude_"); isUUIDLike(cand) {
				rt.nativeID = cand
			}
		}
	}
	h.sessions[id] = rt
	return rt
}

func (h *Hub) ensureLoaded(ctx context.Context, sessionID, projectID string) (*sessionRuntime, error) {
	h.mu.Lock()
	rt := h.getOrCreateRuntimeLocked(sessionID, projectID, "", "")
	if rt.loaded {
		h.mu.Unlock()
		return rt, nil
	}
	agentID := rt.agent
	cwd := rt.cwd
	savedMode := rt.mode
	savedModel := rt.model
	nativeID := rt.nativeID
	nativeInfo, hasNative := h.nativeCache[sessionID]
	h.mu.Unlock()

	spec, ok := findAgentSpec(agentID)
	if !ok {
		return nil, fmt.Errorf("未知的 Agent：%s", agentID)
	}

	switch spec.Protocol {
	case "acp":
		c, err := h.ensureACPClient(agentID)
		if err != nil {
			return nil, err
		}
		if !hasNative {
			for _, item := range h.listACPNativeSessions(ctx, agentID, cwd) {
				if item.ID == sessionID || item.NativeID == sessionID {
					nativeInfo = item
					hasNative = true
					if item.Cwd != "" {
						cwd = item.Cwd
					}
					h.mu.Lock()
					cached := item
					cached.ID = sessionID
					h.nativeCache[sessionID] = cached
					if cwd != "" {
						rt.cwd = cwd
					}
					h.mu.Unlock()
					break
				}
			}
		}
		resetReplay := func() {
			h.mu.Lock()
			rt.replaying = true
			rt.msgs = nil
			rt.replayUser.Reset()
			rt.replayAsst.Reset()
			rt.replayThk.Reset()
			rt.replayTls = nil
			h.mu.Unlock()
		}
		resetReplay()

		loadCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		loadCwd := cwd
		if hasNative && nativeInfo.RawCwd != "" {
			loadCwd = nativeInfo.RawCwd
		}
		var res acpSessionMetaResp
		err = c.call(loadCtx, "session/load", map[string]any{
			"sessionId":  sessionID,
			"cwd":        loadCwd,
			"mcpServers": []any{},
		}, &res)
		if err != nil && cwd != "" && loadCwd != cwd {
			resetReplay()
			err = c.call(loadCtx, "session/load", map[string]any{
				"sessionId":  sessionID,
				"cwd":        cwd,
				"mcpServers": []any{},
			}, &res)
		}

		h.mu.Lock()
		h.flushReplayTurnLocked(rt, true)
		rt.replaying = false
		if err != nil {
			// 若该 ACP 实现不支持 session/load，回退读取本地持久化的历史消息
			if disk := h.loadHistory(sessionID); len(disk) > 0 {
				rt.msgs = disk
				rt.loaded = true
				h.mu.Unlock()
				return rt, nil
			}
			h.mu.Unlock()
			return nil, fmt.Errorf("加载对话失败：%w", err)
		}
		if len(rt.msgs) == 0 {
			if disk := h.loadHistory(sessionID); len(disk) > 0 {
				rt.msgs = disk
			}
		}
		if (rt.title == "" || rt.title == "新对话") && hasNative && nativeInfo.Title != "" {
			rt.title = nativeInfo.Title
		}
		h.captureCapabilitiesLocked(agentID, res)
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
		return rt, nil

	case "codex":
		if nativeID == "" && strings.HasPrefix(sessionID, "codex_") {
			nativeID = strings.TrimPrefix(sessionID, "codex_")
		}
		if (!hasNative || nativeInfo.SourcePath == "") && nativeID != "" {
			if found, ok := findNativeCodexSession(nativeID); ok {
				nativeInfo = found
				hasNative = true
			}
		}
		if hasNative && nativeInfo.Cwd != "" && (cwd == "" || pathMatchKey(cwd) != pathMatchKey(nativeInfo.Cwd)) {
			cwd = nativeInfo.Cwd
		}

		disk := h.loadHistory(sessionID)
		diskMod := h.historyModTime(sessionID)
		if hasNative && nativeInfo.SourcePath != "" {
			if fi, serr := os.Stat(nativeInfo.SourcePath); serr == nil {
				if len(disk) == 0 || fi.ModTime().After(diskMod) {
					if nativeMsgs := loadNativeCodexHistory(nativeInfo.SourcePath); len(nativeMsgs) > 0 {
						disk = nativeMsgs
					}
				}
			}
		}

		codexReady := false
		c, err := h.ensureCodexClient()
		if err != nil {
			if len(disk) == 0 {
				return nil, err
			}
		} else {
			codexReady = true
			if nativeID != "" {
				callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if c.call(callCtx, "thread/resume", map[string]any{"threadId": nativeID}, nil) != nil {
					// 若旧线程已清理，自动新开一个底层 thread
					approval, sb := codexModeParams(savedMode)
					var res struct {
						Thread struct {
							ID string `json:"id"`
						} `json:"thread"`
					}
					if err := c.call(callCtx, "thread/start", map[string]any{
						"cwd":            cwd,
						"approvalPolicy": approval,
						"sandbox":        sb,
					}, &res); err == nil && res.Thread.ID != "" {
						nativeID = res.Thread.ID
					}
				}
				cancel()
			}
		}

		h.mu.Lock()
		rt.nativeID = nativeID
		if cwd != "" {
			rt.cwd = cwd
		}
		if hasNative {
			cached := nativeInfo
			cached.ID = sessionID
			h.nativeCache[sessionID] = cached
			if (rt.title == "" || rt.title == "新对话") && nativeInfo.Title != "" {
				rt.title = nativeInfo.Title
			}
			if (rt.model == "" || rt.model == "default") && nativeInfo.Model != "" {
				rt.model = nativeInfo.Model
			}
			if nativeInfo.UpdatedAt.After(rt.updatedAt) {
				rt.updatedAt = nativeInfo.UpdatedAt
			}
		}
		if len(rt.msgs) == 0 && len(disk) > 0 {
			rt.msgs = disk
		}
		if codexReady {
			rt.loaded = true
		}
		h.mu.Unlock()
		return rt, nil

	case "claude":
		if nativeID == "" && strings.HasPrefix(sessionID, "claude_") {
			if cand := strings.TrimPrefix(sessionID, "claude_"); isUUIDLike(cand) {
				nativeID = cand
			}
		}
		if (!hasNative || nativeInfo.SourcePath == "") && nativeID != "" {
			if found, ok := findNativeClaudeSession(nativeID); ok {
				nativeInfo = found
				hasNative = true
			}
		}
		if hasNative && nativeInfo.Cwd != "" && (cwd == "" || pathMatchKey(cwd) != pathMatchKey(nativeInfo.Cwd)) {
			cwd = nativeInfo.Cwd
		}

		disk := h.loadHistory(sessionID)
		diskMod := h.historyModTime(sessionID)
		if hasNative && nativeInfo.SourcePath != "" {
			if fi, serr := os.Stat(nativeInfo.SourcePath); serr == nil {
				if len(disk) == 0 || fi.ModTime().After(diskMod) {
					if nativeMsgs := loadNativeClaudeHistory(nativeInfo.SourcePath); len(nativeMsgs) > 0 {
						disk = nativeMsgs
					}
				}
			}
		}
		if !hasNative && len(disk) == 0 {
			if _, err := ResolveAgentBinary(agentID); err != nil {
				return nil, err
			}
		}

		h.mu.Lock()
		if nativeID != "" {
			rt.nativeID = nativeID
		}
		if cwd != "" {
			rt.cwd = cwd
		}
		if hasNative {
			cached := nativeInfo
			cached.ID = sessionID
			h.nativeCache[sessionID] = cached
			if (rt.title == "" || rt.title == "新对话") && nativeInfo.Title != "" {
				rt.title = nativeInfo.Title
			}
			if (rt.model == "" || rt.model == "default") && nativeInfo.Model != "" {
				rt.model = nativeInfo.Model
			}
			if nativeInfo.UpdatedAt.After(rt.updatedAt) {
				rt.updatedAt = nativeInfo.UpdatedAt
			}
		}
		if len(rt.msgs) == 0 && len(disk) > 0 {
			rt.msgs = disk
		}
		rt.loaded = true
		h.mu.Unlock()
		return rt, nil

	default:
		if _, err := ResolveAgentBinary(agentID); err != nil {
			return nil, err
		}
		disk := h.loadHistory(sessionID)
		h.mu.Lock()
		if len(rt.msgs) == 0 && len(disk) > 0 {
			rt.msgs = disk
		}
		rt.loaded = true
		h.mu.Unlock()
		return rt, nil
	}
}

func (h *Hub) getOrCreateClaudeRunner(rt *sessionRuntime) (*claudeSessionRunner, error) {
	h.mu.Lock()
	if r := h.claudeRuns[rt.id]; r != nil {
		h.mu.Unlock()
		return r, nil
	}
	cwd, mode, model, nativeID, sid := rt.cwd, rt.mode, rt.model, rt.nativeID, rt.id
	h.mu.Unlock()

	exe, err := ResolveAgentBinary("claude")
	if err != nil {
		return nil, err
	}
	runner := &claudeSessionRunner{
		exe:      exe,
		cwd:      cwd,
		mode:     mode,
		model:    model,
		nativeID: nativeID,
		wrapCmd:  h.cfg.WrapCmd,
		onDelta: func(kind, text string) {
			h.mu.Lock()
			if cur := h.sessions[sid]; cur != nil {
				h.appendDeltaLocked(cur, kind, text)
			}
			h.mu.Unlock()
		},
		onTool: func(tc ToolCall) {
			h.mu.Lock()
			if cur := h.sessions[sid]; cur != nil {
				h.mergeToolCallLocked(cur, tc)
			}
			h.mu.Unlock()
		},
		onPerm: func(ctx context.Context, req permWireRequest) string {
			req.SessionID = sid
			return h.handlePermission(ctx, req)
		},
		onUsage: func(used, size int64) {
			h.mu.Lock()
			if cur := h.sessions[sid]; cur != nil {
				cur.usedTokens = used
				cur.maxTokens = size
				h.broadcastLocked(cur, "session.meta", map[string]any{
					"session":     h.summaryFromRuntimeLocked(cur),
					"used_tokens": cur.usedTokens,
					"max_tokens":  cur.maxTokens,
				})
			}
			h.mu.Unlock()
		},
		onCommands: func(cmds []CommandOption) {
			h.mu.Lock()
			if cap := h.agentCaps["claude"]; cap != nil {
				cap.commands = cmds
			}
			if cur := h.sessions[sid]; cur != nil {
				h.broadcastLocked(cur, "commands", cmds)
			}
			h.mu.Unlock()
		},
		onNativeID: func(nid string) {
			h.mu.Lock()
			if cur := h.sessions[sid]; cur != nil {
				cur.nativeID = nid
			}
			if m, ok := h.meta[sid]; ok {
				m.NativeID = nid
				h.meta[sid] = m
				h.saveMetaLocked()
			}
			h.mu.Unlock()
		},
	}

	h.mu.Lock()
	if existing := h.claudeRuns[sid]; existing != nil {
		h.mu.Unlock()
		return existing, nil
	}
	h.claudeRuns[sid] = runner
	h.mu.Unlock()
	return runner, nil
}

func (h *Hub) getOrCreatePiRunner(rt *sessionRuntime) (*piSessionRunner, error) {
	h.mu.Lock()
	if r := h.piRuns[rt.id]; r != nil {
		h.mu.Unlock()
		return r, nil
	}
	cwd, model, sid := rt.cwd, rt.model, rt.id
	h.mu.Unlock()

	exe, err := ResolveAgentBinary("pi")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(sid))
	sessFile := filepath.Join(h.dataDir, "pi-sessions", hex.EncodeToString(sum[:12])+".jsonl")
	runner := &piSessionRunner{
		exe:         exe,
		cwd:         cwd,
		model:       model,
		sessionFile: sessFile,
		wrapCmd:     h.cfg.WrapCmd,
		onDelta: func(kind, text string) {
			h.mu.Lock()
			if cur := h.sessions[sid]; cur != nil {
				h.appendDeltaLocked(cur, kind, text)
			}
			h.mu.Unlock()
		},
		onTool: func(tc ToolCall) {
			h.mu.Lock()
			if cur := h.sessions[sid]; cur != nil {
				h.mergeToolCallLocked(cur, tc)
			}
			h.mu.Unlock()
		},
	}

	h.mu.Lock()
	if existing := h.piRuns[sid]; existing != nil {
		h.mu.Unlock()
		return existing, nil
	}
	h.piRuns[sid] = runner
	h.mu.Unlock()
	return runner, nil
}

// UpdateSessionConfig 切换某个对话的权限模式、模型或标题。
func (h *Hub) UpdateSessionConfig(ctx context.Context, sessionID, mode, model, title string) (SessionSummary, error) {
	rt, err := h.ensureLoaded(ctx, sessionID, "")
	if err != nil {
		return SessionSummary{}, err
	}
	spec, _ := findAgentSpec(rt.agent)

	mode = strings.TrimSpace(mode)
	model = strings.TrimSpace(model)

	switch spec.Protocol {
	case "acp":
		c, err := h.ensureACPClient(rt.agent)
		if err != nil {
			return SessionSummary{}, err
		}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if mode != "" {
			if err := c.call(callCtx, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": mode}, nil); err != nil {
				return SessionSummary{}, fmt.Errorf("切换模式失败：%w", err)
			}
		}
		if model != "" {
			if err := c.call(callCtx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": model}, nil); err != nil {
				if err2 := c.call(callCtx, "session/set_config_option", map[string]any{"sessionId": sessionID, "configId": "model", "value": model}, nil); err2 != nil {
					return SessionSummary{}, fmt.Errorf("切换模型失败：%w", err)
				}
			}
		}
	case "claude":
		h.mu.Lock()
		runner := h.claudeRuns[sessionID]
		h.mu.Unlock()
		if runner != nil {
			runner.Reconfigure(mode, model)
		}
	case "pi":
		h.mu.Lock()
		runner := h.piRuns[sessionID]
		h.mu.Unlock()
		if runner != nil && model != "" {
			runner.mu.Lock()
			runner.model = model
			runner.mu.Unlock()
			runner.Kill()
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if mode != "" {
		rt.mode = mode
		if cap := h.agentCaps[rt.agent]; cap != nil {
			cap.defMode = mode
		}
	}
	if model != "" {
		rt.model = model
		if cap := h.agentCaps[rt.agent]; cap != nil {
			cap.defModel = model
		}
	}
	if title = strings.TrimSpace(title); title != "" {
		rt.title = truncateRunes(title, 60)
	}
	m := h.meta[sessionID]
	m.ID = sessionID
	m.ProjectID = rt.projectID
	m.Cwd = rt.cwd
	m.Agent = rt.agent
	m.NativeID = rt.nativeID
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
	rt := h.sessions[sessionID]
	agentID := "agy"
	if rt != nil && rt.agent != "" {
		agentID = rt.agent
	} else if m, ok := h.meta[sessionID]; ok && m.Agent != "" {
		agentID = m.Agent
	}
	acpCli := h.acpClients[agentID]
	claudeRun := h.claudeRuns[sessionID]
	delete(h.claudeRuns, sessionID)
	piRun := h.piRuns[sessionID]
	delete(h.piRuns, sessionID)

	delete(h.sessions, sessionID)
	m := h.meta[sessionID]
	m.ID = sessionID
	m.Deleted = true
	m.UpdatedAt = time.Now()
	h.meta[sessionID] = m
	h.saveMetaLocked()
	_ = os.Remove(h.historyFilePath(sessionID))
	if rt != nil {
		for ch := range rt.subs {
			close(ch)
			delete(rt.subs, ch)
		}
	}
	h.mu.Unlock()

	if claudeRun != nil {
		claudeRun.Close()
	}
	if piRun != nil {
		piRun.Close()
	}
	if acpCli != nil && acpCli.alive() {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = acpCli.call(callCtx, "session/close", map[string]any{"sessionId": sessionID}, nil)
		cancel()
	}
	return nil
}

// ---------- 对话收发与事件流 ----------

func (h *Hub) SendPrompt(ctx context.Context, sessionID, projectID, text, behavior string) (SendResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return SendResult{}, errors.New("消息内容不能为空")
	}
	if behavior != "" && behavior != "follow_up" && behavior != "steer" {
		return SendResult{}, errors.New("behavior 只能为空、follow_up 或 steer")
	}
	rt, err := h.ensureLoaded(ctx, sessionID, projectID)
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
		if behavior == "steer" {
			h.interruptCurrentTurnOnly(sessionID)
		}
		return SendResult{Queued: true, Behavior: behavior, QueueItem: &item}, nil
	}

	userMsg := h.startTurnLocked(rt, sessionID, text, now)
	h.saveHistoryLocked(rt)
	h.mu.Unlock()

	go h.runPromptLoop(rt, text)
	return SendResult{Queued: false, Message: &userMsg}, nil
}

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
	m.ID, m.ProjectID, m.Cwd, m.Agent, m.NativeID, m.Title, m.Mode, m.Model, m.Deleted, m.UpdatedAt =
		sessionID, rt.projectID, rt.cwd, rt.agent, rt.nativeID, rt.title, rt.mode, rt.model, false, now
	h.meta[sessionID] = m
	h.saveMetaLocked()

	h.broadcastLocked(rt, "message", userMsg)
	h.broadcastLocked(rt, "status", map[string]any{"busy": true, "title": rt.title})
	return userMsg
}

func (h *Hub) runPromptLoop(rt *sessionRuntime, text string) {
	curText := text
	for curText != "" {
		h.executeOneTurn(rt, curText)

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
		needEnsure := !rt.loaded
		h.mu.Unlock()

		if needEnsure {
			var err error
			rt, err = h.ensureLoaded(context.Background(), rt.id, rt.projectID)
			if err != nil {
				h.mu.Lock()
				rt.msgs = append(rt.msgs, ChatMessage{
					ID:        newShortID("amsg"),
					Role:      "assistant",
					Error:     err.Error(),
					CreatedAt: time.Now(),
				})
				h.saveHistoryLocked(rt)
				h.broadcastLocked(rt, "message", rt.msgs[len(rt.msgs)-1])
				rt.busy = false
				h.broadcastLocked(rt, "status", map[string]any{"busy": false, "title": rt.title})
				h.mu.Unlock()
				return
			}
		}

		h.mu.Lock()
		h.startTurnLocked(rt, rt.id, next.Text, time.Now())
		h.saveHistoryLocked(rt)
		curText = next.Text
		h.mu.Unlock()
	}
}

func (h *Hub) executeOneTurn(rt *sessionRuntime, text string) {
	turnCtx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	rt.turnCancel = cancel
	agentID := rt.agent
	h.mu.Unlock()
	defer cancel()

	spec, _ := findAgentSpec(agentID)
	stopReason := ""
	var err error

	switch spec.Protocol {
	case "acp":
		var c *acpClient
		c, err = h.ensureACPClient(agentID)
		if err == nil {
			var promptRes struct {
				StopReason string `json:"stopReason"`
			}
			err = c.call(turnCtx, "session/prompt", map[string]any{
				"sessionId": rt.id,
				"prompt": []map[string]any{
					{"type": "text", "text": text},
				},
			}, &promptRes)
			stopReason = promptRes.StopReason
		}

	case "claude":
		var runner *claudeSessionRunner
		runner, err = h.getOrCreateClaudeRunner(rt)
		if err == nil {
			stopReason, err = runner.RunTurn(turnCtx, text)
		}

	case "codex":
		var c *codexClient
		c, err = h.ensureCodexClient()
		if err == nil {
			doneCh := make(chan error, 1)
			h.mu.Lock()
			rt.codexTurnDone = doneCh
			threadID := rt.nativeID
			cwd := rt.cwd
			mode := rt.mode
			model := rt.model
			h.mu.Unlock()

			approval, sb := codexModeParams(mode)
			params := map[string]any{
				"threadId":       threadID,
				"cwd":            cwd,
				"approvalPolicy": approval,
				"input": []map[string]any{
					{"type": "text", "text": text},
				},
			}
			_ = sb
			if model != "" && model != "default" {
				params["model"] = model
			}
			var turnRes struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			err = c.call(turnCtx, "turn/start", params, &turnRes)
			if err == nil {
				h.mu.Lock()
				rt.codexTurnID = turnRes.Turn.ID
				h.mu.Unlock()
				select {
				case err = <-doneCh:
				case <-turnCtx.Done():
					stopReason = "cancelled"
					err = turnCtx.Err()
				}
			}
		}

	case "pi":
		var runner *piSessionRunner
		runner, err = h.getOrCreatePiRunner(rt)
		if err == nil {
			stopReason, err = runner.RunTurn(turnCtx, text)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	rt.turnCancel = nil
	rt.codexTurnID = ""
	rt.codexTurnDone = nil

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
	if err != nil && stopReason != "cancelled" && !errors.Is(err, context.Canceled) {
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
		h.saveHistoryLocked(rt)
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
		m.NativeID = rt.nativeID
		h.meta[rt.id] = m
		h.saveMetaLocked()
	}
	h.broadcastLocked(rt, "turn.done", map[string]any{
		"busy":        hasMore,
		"stop_reason": stopReason,
		"error":       errMsg,
	})
}

func (h *Hub) interruptCurrentTurnOnly(sessionID string) {
	h.mu.Lock()
	rt := h.sessions[sessionID]
	if rt == nil {
		h.mu.Unlock()
		return
	}
	agentID := rt.agent
	nativeID := rt.nativeID
	turnID := rt.codexTurnID
	tc := rt.turnCancel
	acpCli := h.acpClients[agentID]
	codexCli := h.codexCli
	claudeRun := h.claudeRuns[sessionID]
	piRun := h.piRuns[sessionID]
	h.mu.Unlock()

	spec, _ := findAgentSpec(agentID)
	switch spec.Protocol {
	case "acp":
		if acpCli != nil && acpCli.alive() {
			_ = acpCli.notify("session/cancel", map[string]any{"sessionId": sessionID})
		}
	case "claude":
		if claudeRun != nil {
			claudeRun.Interrupt()
		}
	case "codex":
		if codexCli != nil && codexCli.alive() && nativeID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = codexCli.call(ctx, "turn/interrupt", map[string]any{"threadId": nativeID, "turnId": turnID}, nil)
			cancel()
		}
	case "pi":
		if piRun != nil {
			piRun.Abort()
		}
	}
	if tc != nil && spec.Protocol != "acp" {
		tc()
	}
}

func (h *Hub) StopPrompt(sessionID string) error {
	h.mu.Lock()
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
	h.interruptCurrentTurnOnly(sessionID)
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

func (h *Hub) Subscribe(ctx context.Context, sessionID, projectID string) (Snapshot, <-chan StreamEvent, func(), error) {
	rt, err := h.ensureLoaded(ctx, sessionID, projectID)
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
	var modes []ModeOption
	var models []ModelOption
	var cmds []CommandOption
	if cap := h.agentCaps[rt.agent]; cap != nil {
		modes = slices.Clone(cap.modes)
		models = slices.Clone(cap.models)
		cmds = slices.Clone(cap.commands)
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
		Modes:       modes,
		Models:      models,
		Commands:    cmds,
	}
}

func (h *Hub) broadcastLocked(rt *sessionRuntime, kind string, data any) {
	rt.seq++
	ev := StreamEvent{Seq: rt.seq, Kind: kind, Time: time.Now(), Data: data}
	for ch := range rt.subs {
		select {
		case ch <- ev:
		default:
			delete(rt.subs, ch)
			close(ch)
		}
	}
}

// ---------- 处理 ACP 推来的通知与权限请求 ----------

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

func (h *Hub) handleACPUpdate(agentID, sessionID string, raw json.RawMessage) {
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
		h.applyReplayUpdateLocked(agentID, rt, u, text)
		return
	}

	switch u.SessionUpdate {
	case "agent_thought_chunk":
		h.appendDeltaLocked(rt, "thinking", text)

	case "agent_message_chunk":
		h.appendDeltaLocked(rt, "text", text)

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
			h.captureCapabilitiesLocked(agentID, acpSessionMetaResp{ConfigOptions: u.ConfigOptions})
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
		if cap := h.agentCaps[agentID]; cap != nil {
			cap.commands = cmds
		}
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

func (h *Hub) applyReplayUpdateLocked(agentID string, rt *sessionRuntime, u acpWireUpdate, text string) {
	switch u.SessionUpdate {
	case "user_message_chunk":
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
		if cap := h.agentCaps[agentID]; cap != nil {
			cap.commands = toCommandOptions(u.AvailableCommands)
		}
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
			for _, k := range []string{"CommandLine", "command", "cmd", "file_path", "AbsolutePath", "TargetFile", "Path", "path", "Query", "query", "Url", "url", "toolSummary"} {
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
			for _, k := range []string{"CommandLine", "command", "file_path", "AbsolutePath", "Path", "path"} {
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

func pathMatchKey(p string) string {
	s := stripPathDecorators(p)
	if s == "" {
		return ""
	}
	if runtime.GOOS == "windows" || !looksLikeWindowsPath(s) {
		s = realOrClean(s)
	}
	return normalizePathMatchKey(s, runtime.GOOS == "windows")
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
