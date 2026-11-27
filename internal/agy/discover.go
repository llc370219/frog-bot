package agy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// AgentCLIView 描述本机/宿主环境上探测到的一个 Agent CLI。
type AgentCLIView struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Installed    bool          `json:"installed"`
	Path         string        `json:"path,omitempty"`
	Version      string        `json:"version,omitempty"`
	Modes        []ModeOption  `json:"modes,omitempty"`
	Models       []ModelOption `json:"models,omitempty"`
	DefaultMode  string        `json:"default_mode,omitempty"`
	DefaultModel string        `json:"default_model,omitempty"`
}

type agentSpec struct {
	ID           string
	Name         string
	BinNames     []string
	EnvOverride  string
	Protocol     string // "acp" | "claude" | "codex" | "pi"
	ACPArgs      []string
	DefaultMode  string
	DefaultModel string
	Modes        []ModeOption
	Models       []ModelOption
}

var agentCatalog = []agentSpec{
	{
		ID:          "agy",
		Name:        "Antigravity",
		BinNames:    []string{"agy", "antigravity"},
		EnvOverride: "FROGBOT_AGY_BIN",
		Protocol:    "acp",
		ACPArgs:     []string{"acp"},
		DefaultMode: "auto",
		Modes:       defaultModes,
	},
	{
		ID:          "claude",
		Name:        "Claude Code",
		BinNames:    []string{"claude"},
		EnvOverride: "FROGBOT_CLAUDE_BIN",
		Protocol:    "claude",
		DefaultMode: "bypassPermissions",
		Modes: []ModeOption{
			{ID: "bypassPermissions", Name: "Auto（自动执行）"},
			{ID: "acceptEdits", Name: "Accept Edits（自动改文件）"},
			{ID: "plan", Name: "Plan（先规划）"},
			{ID: "default", Name: "Default（按需确认）"},
		},
		Models: []ModelOption{
			{ID: "default", Name: "默认模型"},
			{ID: "sonnet", Name: "Claude Sonnet"},
			{ID: "opus", Name: "Claude Opus"},
			{ID: "haiku", Name: "Claude Haiku"},
		},
		DefaultModel: "default",
	},
	{
		ID:          "codex",
		Name:        "Codex",
		BinNames:    []string{"codex"},
		EnvOverride: "FROGBOT_CODEX_BIN",
		Protocol:    "codex",
		DefaultMode: "auto",
		Modes: []ModeOption{
			{ID: "auto", Name: "Auto（工作区自动）"},
			{ID: "full-access", Name: "Full Access（全权限）"},
			{ID: "on-request", Name: "On Request（按需确认）"},
		},
		Models: []ModelOption{
			{ID: "default", Name: "默认模型"},
			{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex"},
			{ID: "gpt-5.4", Name: "GPT-5.4"},
			{ID: "o4-mini", Name: "o4-mini"},
		},
		DefaultModel: "default",
	},
	{
		ID:          "pi",
		Name:        "Pi",
		BinNames:    []string{"pi"},
		EnvOverride: "FROGBOT_PI_BIN",
		Protocol:    "pi",
		DefaultMode: "auto",
		Modes: []ModeOption{
			{ID: "auto", Name: "Auto（自动执行）"},
		},
	},
	{
		ID:          "gemini",
		Name:        "Gemini CLI",
		BinNames:    []string{"gemini"},
		EnvOverride: "FROGBOT_GEMINI_BIN",
		Protocol:    "acp",
		ACPArgs:     []string{"--experimental-acp"},
		DefaultMode: "auto",
		Modes:       defaultModes,
	},
	{
		ID:          "opencode",
		Name:        "OpenCode",
		BinNames:    []string{"opencode"},
		EnvOverride: "FROGBOT_OPENCODE_BIN",
		Protocol:    "acp",
		ACPArgs:     []string{"acp"},
		DefaultMode: "auto",
		Modes:       defaultModes,
	},
	{
		ID:          "grok",
		Name:        "Grok CLI",
		BinNames:    []string{"grok"},
		EnvOverride: "FROGBOT_GROK_BIN",
		Protocol:    "acp",
		ACPArgs:     []string{"acp"},
		DefaultMode: "auto",
		Modes:       defaultModes,
	},
}

var (
	extraEnvMu sync.RWMutex
	extraEnvFn func() map[string]string
)

// SetExtraEnvProvider registers a host callback supplying additional CLI auth env vars
// (e.g. saved official CLI tokens / API keys from the host encrypted vault).
func SetExtraEnvProvider(fn func() map[string]string) {
	extraEnvMu.Lock()
	extraEnvFn = fn
	extraEnvMu.Unlock()
}

// FindHostBinary locates any of binNames on PATH or common user/system installation directories.
func FindHostBinary(binNames []string) string {
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".cmd", ".bat", ""}
	}
	for _, bin := range binNames {
		if p, err := exec.LookPath(bin); err == nil && isExecFile(p) {
			return p
		}
	}
	dirs := hostSearchDirs()
	for _, bin := range binNames {
		for _, dir := range dirs {
			for _, ext := range exts {
				cand := filepath.Join(dir, bin+ext)
				if isExecFile(cand) {
					return cand
				}
			}
		}
	}
	return ""
}

func findAgentSpec(id string) (agentSpec, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, s := range agentCatalog {
		if s.ID == id {
			return s, true
		}
	}
	return agentSpec{}, false
}

var (
	loginPathOnce sync.Once
	cachedShellP  []string
)

func probeLoginShellPath() []string {
	loginPathOnce.Do(func() {
		if runtime.GOOS == "windows" {
			return
		}
		sh := strings.TrimSpace(os.Getenv("SHELL"))
		if sh == "" || strings.Contains(sh, "nologin") || strings.Contains(sh, "false") {
			for _, cand := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
				if isExecFile(cand) {
					sh = cand
					break
				}
			}
		}
		if sh == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, sh, "-lc", `printf %s "$PATH"`).Output()
		if err != nil || len(out) == 0 {
			return
		}
		for _, p := range filepath.SplitList(strings.TrimSpace(string(out))) {
			if p != "" {
				cachedShellP = append(cachedShellP, p)
			}
		}
	})
	return cachedShellP
}

// hostSearchDirs 汇总本机所有常见的用户级与系统级 CLI 安装目录（nvm / fnm / volta / bun / pnpm / cargo / homebrew / ~/.local/bin 等）。
func hostSearchDirs() []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		dirs = append(dirs, p)
	}

	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		add(p)
	}
	for _, p := range probeLoginShellPath() {
		add(p)
	}

	var homes []string
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		homes = append(homes, h)
	}
	if runtime.GOOS == "linux" {
		if !slices.Contains(homes, "/root") {
			homes = append(homes, "/root")
		}
		if entries, err := os.ReadDir("/home"); err == nil {
			for _, e := range entries {
				if e.IsDir() && e.Name() != "frogbot" {
					p := filepath.Join("/home", e.Name())
					if !slices.Contains(homes, p) {
						homes = append(homes, p)
					}
				}
			}
		}
	}

	for _, home := range homes {
		for _, rel := range []string{
			filepath.Join(".local", "bin"),
			"bin",
			filepath.Join(".antigravity", "bin"),
			filepath.Join(".npm-global", "bin"),
			filepath.Join(".yarn", "bin"),
			filepath.Join(".bun", "bin"),
			filepath.Join(".cargo", "bin"),
			filepath.Join(".volta", "bin"),
			filepath.Join(".asdf", "shims"),
			filepath.Join(".mise", "shims"),
			filepath.Join(".local", "share", "mise", "shims"),
			filepath.Join(".local", "share", "pnpm"),
			filepath.Join("Library", "pnpm"),
			filepath.Join(".opencode", "bin"),
		} {
			add(filepath.Join(home, rel))
		}
		// NVM 与 FNM 多版本目录（按版本号倒序优先取新版 Node）
		for _, pattern := range []string{
			filepath.Join(home, ".nvm", "versions", "node", "*", "bin"),
			filepath.Join(home, ".local", "share", "fnm", "node-versions", "*", "installation", "bin"),
			filepath.Join(home, "Library", "Application Support", "fnm", "node-versions", "*", "installation", "bin"),
		} {
			if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
				sort.Sort(sort.Reverse(sort.StringSlice(matches)))
				for _, m := range matches {
					add(m)
				}
			}
		}
	}

	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			add(filepath.Join(appData, "npm"))
		}
		if localApp := os.Getenv("LOCALAPPDATA"); localApp != "" {
			add(filepath.Join(localApp, "Programs", "Antigravity", "bin"))
			add(filepath.Join(localApp, "pnpm"))
		}
		if up := os.Getenv("USERPROFILE"); up != "" {
			add(filepath.Join(up, "scoop", "shims"))
		}
	} else {
		for _, sysDir := range []string{
			"/opt/homebrew/bin",
			"/usr/local/bin",
			"/usr/bin",
			"/bin",
			"/snap/bin",
		} {
			add(sysDir)
		}
	}
	return dirs
}

// ResolveAgentBinary 在宿主环境中查找指定 Agent CLI 的可执行文件路径。
func ResolveAgentBinary(agentID string) (string, error) {
	spec, ok := findAgentSpec(agentID)
	if !ok {
		return "", fmt.Errorf("不支持的 Agent CLI：%s", agentID)
	}
	if spec.EnvOverride != "" {
		if custom := strings.TrimSpace(os.Getenv(spec.EnvOverride)); custom != "" {
			if isExecFile(custom) {
				return custom, nil
			}
			return "", fmt.Errorf("%s 不可执行：%s", spec.EnvOverride, custom)
		}
	}

	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".cmd", ".bat", ""}
	}

	for _, bin := range spec.BinNames {
		if p, err := exec.LookPath(bin); err == nil && isExecFile(p) {
			return p, nil
		}
	}
	dirs := hostSearchDirs()
	for _, bin := range spec.BinNames {
		for _, dir := range dirs {
			for _, ext := range exts {
				cand := filepath.Join(dir, bin+ext)
				if isExecFile(cand) {
					return cand, nil
				}
			}
		}
	}
	return "", fmt.Errorf("本机未检测到 %s（%s）", spec.Name, spec.BinNames[0])
}

// DiscoverAgents 检测宿主机器上所有已安装的受支持 Agent CLI。
func DiscoverAgents() []AgentCLIView {
	out := make([]AgentCLIView, 0, len(agentCatalog))
	for _, spec := range agentCatalog {
		p, err := ResolveAgentBinary(spec.ID)
		v := AgentCLIView{
			ID:           spec.ID,
			Name:         spec.Name,
			Installed:    err == nil,
			Path:         p,
			Modes:        slices.Clone(spec.Modes),
			Models:       slices.Clone(spec.Models),
			DefaultMode:  spec.DefaultMode,
			DefaultModel: spec.DefaultModel,
		}
		out = append(out, v)
	}
	return out
}

// buildChildEnv 为外部 CLI 子进程构建完整宿主环境（保留宿主 HOME / 登录凭证，自动把 CLI 所在目录与 node 工具链加入 PATH，同时剔除 frog 自身敏感私钥）。
func buildChildEnv(exePath string) []string {
	drop := map[string]bool{
		"FROGBOT_MASTER_KEY": true,
		"FROGBOT_API_KEY":    true,
		"FROGBOT_PASSWORD":   true,
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+3)
	hasHome := false
	for _, kv := range base {
		k, _, _ := cutString(kv, '=')
		if drop[k] || strings.EqualFold(k, "PATH") {
			continue
		}
		if k == "HOME" {
			hasHome = true
		}
		out = append(out, kv)
	}
	if !hasHome {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			out = append(out, "HOME="+h)
		}
	}

	seen := map[string]bool{}
	var pathParts []string
	addP := func(p string) {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			pathParts = append(pathParts, p)
		}
	}
	if exePath != "" {
		addP(filepath.Dir(exePath))
	}
	for _, d := range hostSearchDirs() {
		addP(d)
	}
	out = append(out, "PATH="+strings.Join(pathParts, string(os.PathListSeparator)))
	extraEnvMu.RLock()
	fn := extraEnvFn
	extraEnvMu.RUnlock()
	if fn != nil {
		existing := map[string]bool{}
		for _, kv := range out {
			if k, _, ok := strings.Cut(kv, "="); ok {
				existing[k] = true
			}
		}
		for k, v := range fn() {
			if k != "" && v != "" && !existing[k] {
				out = append(out, k+"="+v)
			}
		}
	}
	return out
}

func probeCLIVersion(exePath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, exePath, "--version")
	cmd.Env = buildChildEnv(exePath)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line := firstNonEmptyLine(string(bytes.TrimSpace(out)))
	return truncateRunes(strings.TrimSpace(line), 48)
}
