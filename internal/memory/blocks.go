// Package memory implements the Frog Bot V2 Hermes-style bounded memory,
// prompt-cache-locked static snapshots, pure-Go SQLite FTS5 (trigram) Chinese
// history archive, long-text pointerization, and SKILL.md library.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"frogbot/internal/atomicfile"
)

const (
	UserMaxChars   = 1400 // Hard rune limit for shared USER.md
	MemoryMaxChars = 2200 // Hard rune limit for per-bot MEMORY.md
	WarnRatio      = 0.90 // Emit compression reminder at >= 90% capacity
)

var (
	ErrEntryNotFound    = errors.New("memory entry not found")
	ErrCapacityExceeded = errors.New("memory block capacity exceeded")
	entryLineRe         = regexp.MustCompile(`^\[#([a-zA-Z0-9_-]{3,16})\]\s*(.+)$`)
)

// Entry is a single addressable memory item stored as `[#id] content`.
type Entry struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// BlockUsage reports rune consumption against the block's hard limit.
type BlockUsage struct {
	Target       string  `json:"target"`
	Entries      int     `json:"entries"`
	UsedChars    int     `json:"used_chars"`
	MaxChars     int     `json:"max_chars"`
	Ratio        float64 `json:"ratio"`
	NearCapacity bool    `json:"near_capacity"`
}

// ManageResult is returned to the Bot after a memory_manage mutation.
type ManageResult struct {
	Action  string     `json:"action"`
	Target  string     `json:"target"`
	Entry   *Entry     `json:"entry,omitempty"`
	Usage   BlockUsage `json:"usage"`
	Warning string     `json:"warning,omitempty"`
	Note    string     `json:"note,omitempty"`
}

// pathLocks serializes concurrent file edits across multiple Bots/goroutines targeting the same file (e.g. shared USER.md).
var (
	pathLocksMu sync.Mutex
	pathLocks   = map[string]*sync.Mutex{}
)

func lockPath(p string) *sync.Mutex {
	abs, err := filepath.Abs(p)
	if err == nil {
		p = abs
	}
	pathLocksMu.Lock()
	defer pathLocksMu.Unlock()
	m := pathLocks[p]
	if m == nil {
		m = &sync.Mutex{}
		pathLocks[p] = m
	}
	return m
}

func newShortID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// BlockStore manages a single bounded `[#id] content` memory markdown file (USER.md or MEMORY.md).
type BlockStore struct {
	Target   string
	Path     string
	MaxChars int
}

func NewBlockStore(target, path string, maxChars int) *BlockStore {
	if maxChars <= 0 {
		if target == "user" {
			maxChars = UserMaxChars
		} else {
			maxChars = MemoryMaxChars
		}
	}
	return &BlockStore{Target: target, Path: path, MaxChars: maxChars}
}

func parseEntries(raw string) []Entry {
	var out []Entry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := entryLineRe.FindStringSubmatch(line); len(m) == 3 {
			out = append(out, Entry{ID: m[1], Content: strings.TrimSpace(m[2])})
			continue
		}
		// Migrate plain bullet/text lines cleanly into entry-ID format.
		plain := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "- "), "* "))
		if plain != "" {
			out = append(out, Entry{ID: newShortID(), Content: plain})
		}
	}
	return out
}

func renderEntries(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "[#%s] %s\n", e.ID, strings.TrimSpace(e.Content))
	}
	return sb.String()
}

func (s *BlockStore) readLocked() ([]Entry, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseEntries(string(raw)), nil
}

func (s *BlockStore) writeLocked(entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.Path, []byte(renderEntries(entries)), 0o600)
}

func (s *BlockStore) usageOf(entries []Entry) BlockUsage {
	used := utf8.RuneCountInString(strings.TrimSpace(renderEntries(entries)))
	ratio := 0.0
	if s.MaxChars > 0 {
		ratio = float64(used) / float64(s.MaxChars)
	}
	return BlockUsage{
		Target:       s.Target,
		Entries:      len(entries),
		UsedChars:    used,
		MaxChars:     s.MaxChars,
		Ratio:        ratio,
		NearCapacity: ratio >= WarnRatio,
	}
}

func (s *BlockStore) List() ([]Entry, BlockUsage, error) {
	mu := lockPath(s.Path)
	mu.Lock()
	defer mu.Unlock()
	entries, err := s.readLocked()
	if err != nil {
		return nil, BlockUsage{}, err
	}
	return entries, s.usageOf(entries), nil
}

func (s *BlockStore) Render() (string, error) {
	entries, _, err := s.List()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(renderEntries(entries)), nil
}

// Manage performs atomic entry-ID add / update / remove operations under a per-file mutex.
func (s *BlockStore) Manage(action, entryID, content string) (ManageResult, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	entryID = strings.TrimPrefix(strings.TrimSpace(entryID), "#")
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")

	mu := lockPath(s.Path)
	mu.Lock()
	defer mu.Unlock()

	entries, err := s.readLocked()
	if err != nil {
		return ManageResult{}, err
	}
	var affected *Entry

	switch action {
	case "add":
		if content == "" {
			return ManageResult{}, errors.New("content 不能为空")
		}
		id := entryID
		if id == "" {
			id = newShortID()
		}
		for _, e := range entries {
			if e.ID == id {
				id = newShortID()
				break
			}
		}
		e := Entry{ID: id, Content: content}
		entries = append(entries, e)
		affected = &e
	case "update":
		if entryID == "" || content == "" {
			return ManageResult{}, errors.New("update 需要 entry_id 和 content")
		}
		found := false
		for i := range entries {
			if entries[i].ID == entryID {
				entries[i].Content = content
				cp := entries[i]
				affected = &cp
				found = true
				break
			}
		}
		if !found {
			return ManageResult{}, fmt.Errorf("%w: %s", ErrEntryNotFound, entryID)
		}
	case "remove", "delete":
		if entryID == "" {
			return ManageResult{}, errors.New("remove 需要 entry_id")
		}
		found := false
		next := make([]Entry, 0, len(entries))
		for _, e := range entries {
			if e.ID == entryID {
				cp := e
				affected = &cp
				found = true
				continue
			}
			next = append(next, e)
		}
		if !found {
			return ManageResult{}, fmt.Errorf("%w: %s", ErrEntryNotFound, entryID)
		}
		entries = next
	default:
		return ManageResult{}, fmt.Errorf("未知 action: %s（仅支持 add|update|remove）", action)
	}

	usage := s.usageOf(entries)
	if usage.UsedChars > s.MaxChars {
		return ManageResult{Action: action, Target: s.Target, Usage: usage},
			fmt.Errorf("%w: %s 已达 %d/%d 字符，请先用 update/remove 精简旧条目再添加", ErrCapacityExceeded, s.Target, usage.UsedChars, s.MaxChars)
	}
	if err := s.writeLocked(entries); err != nil {
		return ManageResult{}, err
	}
	res := ManageResult{
		Action: action,
		Target: s.Target,
		Entry:  affected,
		Usage:  usage,
		Note:   "已原子落盘；为保持 Prompt Cache 命中率，修改将在下一轮新会话刷新进静态系统前缀。",
	}
	if usage.NearCapacity {
		res.Warning = fmt.Sprintf("提醒：%s 容量已达 %.0f%%（%d/%d 字），建议尽快合并或清理过期条目。", s.Target, usage.Ratio*100, usage.UsedChars, usage.MaxChars)
	}
	return res, nil
}

// HermesManager coordinates USER.md, MEMORY.md, SOUL.md, Skills, HistoryDB, Pointerizer,
// and an immutable SessionPromptSnapshot to guarantee 100% Prompt Cache stability during a session.
type HermesManager struct {
	UserBlock   *BlockStore
	MemoryBlock *BlockStore
	SoulPath    string
	Skills      *SkillStore
	History     *HistoryStore
	Pointer     *Pointerizer

	mu             sync.RWMutex
	frozenSnapshot string
	snapshotDirty  bool
}

func NewHermesManager(userPath, memoryPath, soulPath, skillsDir, historyDBPath, workspace string, sum Summarizer) (*HermesManager, error) {
	hist, err := OpenHistoryStore(historyDBPath, sum)
	if err != nil {
		return nil, err
	}
	return &HermesManager{
		UserBlock:   NewBlockStore("user", userPath, UserMaxChars),
		MemoryBlock: NewBlockStore("memory", memoryPath, MemoryMaxChars),
		SoulPath:    soulPath,
		Skills:      NewSkillStore(skillsDir),
		History:     hist,
		Pointer:     NewPointerizer(workspace, sum),
	}, nil
}

func (m *HermesManager) Close() error {
	if m.History != nil {
		return m.History.Close()
	}
	return nil
}

// ManageMemory dispatches memory_manage(action, target, entry_id, content) and marks
// the static snapshot dirty for the NEXT session without mutating the current frozen prefix.
func (m *HermesManager) ManageMemory(action, target, entryID, content string) (ManageResult, error) {
	var bs *BlockStore
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "user", "shared":
		bs = m.UserBlock
	case "", "memory", "private":
		bs = m.MemoryBlock
	default:
		return ManageResult{}, fmt.Errorf("未知 target: %s（仅支持 user|memory）", target)
	}
	res, err := bs.Manage(action, entryID, content)
	if err == nil {
		m.mu.Lock()
		m.snapshotDirty = true
		m.mu.Unlock()
	}
	return res, err
}

// FrozenSystemPrefix builds and freezes the static System Prompt prefix once per session:
// basePrompt + SOUL.md + USER.md + MEMORY.md + static skills index + static peer directory.
// Subsequent calls within the same session return the exact byte-identical string to lock Prompt Cache.
func (m *HermesManager) FrozenSystemPrefix(basePrompt, staticPeerDirectory string) string {
	m.mu.RLock()
	if m.frozenSnapshot != "" {
		s := m.frozenSnapshot
		m.mu.RUnlock()
		return s
	}
	m.mu.RUnlock()
	return m.RefreshSnapshot(basePrompt, staticPeerDirectory)
}

// RefreshSnapshot rebuilds and locks a fresh static System Prompt prefix for a new session.
func (m *HermesManager) RefreshSnapshot(basePrompt, staticPeerDirectory string) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(basePrompt))

	if m.SoulPath != "" {
		if raw, err := os.ReadFile(m.SoulPath); err == nil {
			if soul := strings.TrimSpace(string(raw)); soul != "" {
				sb.WriteString("\n\n## SOUL\n")
				sb.WriteString(soul)
			}
		}
	}
	if m.UserBlock != nil {
		if u, err := m.UserBlock.Render(); err == nil && u != "" {
			sb.WriteString("\n\n## USER.md（全局用户画像，仅通过 memory_manage 维护）\n")
			sb.WriteString(u)
		}
	}
	if m.MemoryBlock != nil {
		if mem, err := m.MemoryBlock.Render(); err == nil && mem != "" {
			sb.WriteString("\n\n## MEMORY.md（本助理核心记忆，仅通过 memory_manage 维护）\n")
			sb.WriteString(mem)
		}
	}
	if m.Skills != nil {
		if idx := m.Skills.StaticIndex(); idx != "" {
			sb.WriteString("\n\n## 技能索引（按需调用 skill_load 展开）\n")
			sb.WriteString(idx)
		}
	}
	if p := strings.TrimSpace(staticPeerDirectory); p != "" {
		sb.WriteString("\n\n## 跨服静态节点名录\n")
		sb.WriteString(p)
	}

	snap := sb.String()
	m.mu.Lock()
	m.frozenSnapshot = snap
	m.snapshotDirty = false
	m.mu.Unlock()
	return snap
}

// SnapshotDirty reports whether memory/skills changed since the current session snapshot was frozen.
func (m *HermesManager) SnapshotDirty() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshotDirty
}
