package onefrog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

const CLITool = "run_agent_cli"

type CLIStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	// Subscription 是这个 CLI 登录后可用的官方订阅；Login 是在设备终端完成登录的命令。
	Subscription string `json:"subscription"`
	Login        string `json:"login"`
	// ModelOption 表示 Frog Bot 可以给这个 CLI 指定模型（否则用 CLI 自己的默认模型）。
	ModelOption bool     `json:"model_option"`
	Models      []string `json:"models,omitempty"` // 常用模型（仅作建议，也可手填）
	// Path 是检测到的可执行文件；Managed 表示是 frog 一键安装的（在 <data>/tools 里）。
	Path    string `json:"path,omitempty"`
	Managed bool   `json:"managed,omitempty"`
	// Installable 表示这台机器可以在设置里一键安装/更新；Install 是最近一次安装的进度。
	Installable bool          `json:"installable"`
	Install     *InstallState `json:"install,omitempty"`
}

var cliNames = []CLIStatus{
	{ID: "codex", Name: "Codex CLI", Subscription: "ChatGPT Plus / Pro / Business", Login: "codex login", ModelOption: true},
	{ID: "claude", Name: "Claude Code", Subscription: "Claude Pro / Max", Login: "claude（首次运行后输入 /login）", ModelOption: true, Models: []string{"sonnet", "opus", "haiku"}},
	{ID: "pi", Name: "Pi", Subscription: "Claude Pro / Max、ChatGPT 等", Login: "pi（运行后输入 /login）", ModelOption: true},
	{ID: "grok", Name: "Grok CLI", Subscription: "SuperGrok / X Premium", Login: "grok login"},
	{ID: "agy", Name: "Antigravity CLI", Subscription: "Google 账户", Login: "agy login"},
}

// CLI 认证方式：subscription（默认）只用 CLI 自己登录的官方账户，启动时去掉会让 CLI 改走按量计费的
// API Key 环境变量；env 原样保留 Frog Bot 进程的环境变量（例如你在服务器上配置了 ANTHROPIC_API_KEY）。
const (
	CLIAuthSubscription = "subscription"
	CLIAuthEnv          = "env"
)

// CLIOptions 是某个 Bot 给 CLI 的额外设置。
type CLIOptions struct {
	Model string `json:"cli_model,omitempty"`
	Auth  string `json:"cli_auth,omitempty"`
}

var cliModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,99}$`)

// ValidateCLIOptions 检查并规整 CLI 设置；不支持指定模型的 CLI 会忽略模型。
func ValidateCLIOptions(id string, o CLIOptions) (CLIOptions, error) {
	o.Model = strings.TrimSpace(o.Model)
	switch o.Auth {
	case "":
		o.Auth = CLIAuthSubscription
	case CLIAuthSubscription, CLIAuthEnv:
	default:
		return CLIOptions{}, errors.New("CLI 认证方式只能是官方订阅或环境变量")
	}
	if id == "" {
		return CLIOptions{}, nil
	}
	if !cliStatus(id).ModelOption {
		o.Model = ""
	}
	if o.Model != "" && !cliModelPattern.MatchString(o.Model) {
		return CLIOptions{}, errors.New("模型名只能包含字母、数字和 . _ - : / @ [ ]，且不能以符号开头")
	}
	return o, nil
}

func cliStatus(id string) CLIStatus {
	for _, cli := range cliNames {
		if cli.ID == id {
			return cli
		}
	}
	return CLIStatus{}
}

// subscriptionEnv 是选择「官方订阅」时不传给 CLI 的环境变量：有这些变量时 CLI 会改用 API Key
// （按量计费）或第三方地址，而不是你登录的订阅账户。
var subscriptionEnv = map[string][]string{
	"codex":  {"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY"},
	"claude": {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"},
	"pi":     {"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY"},
	"grok":   {"XAI_API_KEY", "GROK_API_KEY", "GROK_BASE_URL"},
	"agy":    {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
}

func CLIName(id string) string { return cliStatus(id).Name }

func DetectCLIs() []CLIStatus {
	status := append([]CLIStatus(nil), cliNames...)
	managed := managedCLIBin()
	for i := range status {
		c := &status[i]
		path, _, err := resolveCLI(c.ID)
		c.Installed = err == nil
		if c.Installed {
			c.Path = path
			c.Managed = managed != "" && filepath.Dir(path) == managed
		}
		c.Installable = installSupported(c.ID) == nil
		c.Install = installSnapshot(c.ID)
	}
	return status
}

func resolveCLI(id string) (string, []string, error) {
	if CLIName(id) == "" {
		return "", nil, fmt.Errorf("不支持的 Agent CLI：%s", id)
	}
	if runtime.GOOS != "windows" {
		// frog 一键安装的版本优先：在设置里点了「更新」，用的就应该是它。
		if m := managedCLIBin(); m != "" {
			if p := filepath.Join(m, id); isExecutable(p) {
				return p, nil, nil
			}
		}
		if path, err := exec.LookPath(id); err == nil {
			return path, nil, nil
		}
		// 服务以 systemd 启动时 PATH 只有系统目录，不会加载 ~/.bashrc 或 nvm，
		// 而服务用户没有 root，CLI 通常只能装在自己的家目录里，所以再找一遍这些位置。
		for _, dir := range userBinDirs() {
			if p := filepath.Join(dir, id); isExecutable(p) {
				return p, nil, nil
			}
		}
		return "", nil, exec.ErrNotFound
	}
	if path, err := exec.LookPath(id + ".exe"); err == nil {
		return path, nil, nil
	}
	// npm installs .cmd shims on Windows. Launch the known package entry point
	// directly; cmd.exe would interpret shell characters in user prompts.
	shim, err := exec.LookPath(id + ".cmd")
	if err != nil {
		return "", nil, err
	}
	root := filepath.Join(filepath.Dir(shim), "node_modules")
	if id == "claude" {
		p := filepath.Join(root, "@anthropic-ai", "claude-code", "bin", "claude.exe")
		if isFile(p) {
			return p, nil, nil
		}
	}
	var entries []string
	switch id {
	case "codex":
		entries = []string{"@openai/codex/bin/codex.js"}
	case "claude":
		entries = []string{"@anthropic-ai/claude-code/cli.js"}
	case "pi":
		entries = []string{"@earendil-works/pi-coding-agent/dist/bundle/cli.js", "@earendil-works/pi-coding-agent/dist/cli.js", "@mariozechner/pi-coding-agent/dist/cli.js"}
	}
	for _, entry := range entries {
		if p := filepath.Join(root, entry); isFile(p) {
			node := filepath.Join(filepath.Dir(shim), "node.exe")
			if !isFile(node) {
				node, err = exec.LookPath("node.exe")
				if err != nil {
					return "", nil, err
				}
			}
			return node, []string{p}, nil
		}
	}
	return "", nil, errors.New("未找到 CLI 可执行文件")
}

// userBinDirs 是用户级安装常见的可执行文件目录（不需要 root）：~/.local/bin、npm 自定义前缀、
// nvm 装的各个 Node 版本（新版本在前）、Volta、Bun 等。
func userBinDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		filepath.Join(home, ".npm-global", "bin"),
	}
	if prefix := npmrcPrefix(filepath.Join(home, ".npmrc"), home); prefix != "" {
		dirs = append(dirs, filepath.Join(prefix, "bin"))
	}
	nvmDir := os.Getenv("NVM_DIR")
	if nvmDir == "" {
		nvmDir = filepath.Join(home, ".nvm")
	}
	dirs = append(dirs, nvmBinDirs(nvmDir)...)
	return append(dirs,
		filepath.Join(home, ".volta", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".local", "share", "pnpm"),
	)
}

// npmrcPrefix 读出 ~/.npmrc 里的 prefix=（npm config set prefix 写的），没有就返回空。
func npmrcPrefix(path, home string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(k) != "prefix" {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if strings.HasPrefix(v, "~/") {
			v = filepath.Join(home, v[2:])
		}
		if filepath.IsAbs(v) {
			return v
		}
	}
	return ""
}

// nvmBinDirs 列出 nvm 装的每个 Node 版本的 bin 目录，版本新的在前。
func nvmBinDirs(nvmDir string) []string {
	entries, err := os.ReadDir(filepath.Join(nvmDir, "versions", "node"))
	if err != nil {
		return nil
	}
	type ver struct {
		dir string
		n   [3]int
	}
	var vs []ver
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var v ver
		if _, err := fmt.Sscanf(e.Name(), "v%d.%d.%d", &v.n[0], &v.n[1], &v.n[2]); err != nil {
			continue
		}
		v.dir = filepath.Join(nvmDir, "versions", "node", e.Name(), "bin")
		vs = append(vs, v)
	}
	slices.SortFunc(vs, func(a, b ver) int {
		for i := range a.n {
			if a.n[i] != b.n[i] {
				return b.n[i] - a.n[i]
			}
		}
		return 0
	})
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.dir
	}
	return out
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// withPathDir 把 dir 放到 PATH 最前面（已经在 PATH 里就不动）。npm 装的 CLI 用
// `#!/usr/bin/env node` 启动，node 和 CLI 在同一个 bin 目录，不在 PATH 里就跑不起来。
func withPathDir(env []string, dir string) []string {
	for i, kv := range env {
		key, val, _ := strings.Cut(kv, "=")
		if key != "PATH" {
			continue
		}
		if slices.Contains(filepath.SplitList(val), dir) {
			return env
		}
		out := slices.Clone(env)
		if val == "" {
			out[i] = "PATH=" + dir
		} else {
			out[i] = "PATH=" + dir + string(os.PathListSeparator) + val
		}
		return out
	}
	return append(slices.Clone(env), "PATH="+dir)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func cliArgs(id, prompt, model string) (args []string, stdin string) {
	switch id {
	case "codex":
		args = []string{"exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "--json"}
		if model != "" {
			args = append(args, "--model", model)
		}
		return append(args, "-"), prompt
	case "claude":
		args = []string{"-p", "--output-format", "text"}
		if model != "" {
			args = append(args, "--model", model)
		}
		return append(args, "请根据标准输入中的任务说明执行并回复。"), prompt
	case "pi":
		args = []string{"--print", "--no-session"}
		if model != "" {
			args = append(args, "--model", model)
		}
		return args, prompt
	case "grok":
		return []string{"--no-auto-update", "--output-format", "plain", "-p", prompt}, ""
	case "agy":
		return []string{"--output-format", "json", "-p", prompt}, ""
	default:
		return nil, ""
	}
}

func runCLI(ctx context.Context, id, workspace, prompt string, opts CLIOptions) (string, error) {
	name := CLIName(id)
	exe, prefix, err := resolveCLI(id)
	if err != nil {
		return "", fmt.Errorf("找不到 %s。请在「设置 → 模型与 Agent」里一键安装，或在 frog 所在机器手动安装并放进 PATH", name)
	}
	if opts, err = ValidateCLIOptions(id, opts); err != nil {
		return "", err
	}
	args, stdin := cliArgs(id, prompt, opts.Model)
	if stdin == "" && len(prompt) > 24_000 {
		return "", fmt.Errorf("%s 的任务说明过长，请缩短后重试", name)
	}
	cmd := exec.CommandContext(ctx, exe, append(prefix, args...)...)
	if err := sandboxed(cmd); err != nil {
		return "", err
	}
	cmd.Dir = workspace
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Env = cliEnv(cmd.Environ(), id, opts.Auth)
	if runtime.GOOS != "windows" {
		// frog 一键安装的 CLI 用 frog 自己装的 Node 运行。
		if m := managedCLIBin(); m != "" && filepath.Dir(exe) == m {
			cmd.Env = withPathDir(cmd.Env, managedNodeBin())
		}
		cmd.Env = withPathDir(cmd.Env, filepath.Dir(exe))
	}
	var stderr, stdout cliBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	var codex codexOutput
	if id == "codex" {
		cmd.Stdout = &codex
	}
	cmd.WaitDelay = 3 * time.Second
	setCLIProcessGroup(cmd)
	err = cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	out := strings.TrimSpace(stdout.String())
	var details string
	switch id {
	case "codex":
		codex.consumeLine()
		out, details = codex.text, codex.err
		if codex.failed && err == nil {
			err = errors.New("任务未完成")
		}
	case "agy":
		var result struct {
			Status, Response, Error string
		}
		if json.Unmarshal([]byte(out), &result) == nil {
			out, details = strings.TrimSpace(result.Response), result.Error
			if result.Status != "SUCCESS" && err == nil {
				err = fmt.Errorf("运行状态：%s", result.Status)
			}
		} else if err == nil {
			err = errors.New("无法读取 CLI 的返回结果")
		}
	}
	if err != nil {
		if details == "" {
			details = strings.TrimSpace(stderr.String())
		}
		if details == "" {
			details = out
		}
		if details != "" {
			return "", fmt.Errorf("%s 运行失败：%s", name, Truncate(details, 1400))
		}
		return "", fmt.Errorf("%s 运行失败：%w", name, err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%s 没有返回回复，请检查它的登录和模型配置", name)
	}
	return Truncate(out, 16000), nil
}

// cliEnv keeps the CLI's own authentication but never passes Frog Bot's
// credentials. In subscription mode it also drops the vendor API-key variables,
// so the CLI uses the account logged in on this device.
func cliEnv(environ []string, id, auth string) []string {
	drop := map[string]bool{"FROGBOT_MASTER_KEY": true, "FROGBOT_API_KEY": true, "FROGBOT_PASSWORD": true, "API_KEY": true}
	if auth != CLIAuthEnv {
		for _, k := range subscriptionEnv[id] {
			drop[k] = true
		}
	}
	env := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if !drop[strings.ToUpper(key)] {
			env = append(env, kv)
		}
	}
	return env
}

const cliOutputLimit = 1 << 20

type cliBuffer struct{ bytes.Buffer }

func (b *cliBuffer) Write(p []byte) (int, error) {
	if left := cliOutputLimit - b.Len(); left > 0 {
		_, _ = b.Buffer.Write(p[:min(left, len(p))])
	}
	return len(p), nil
}

// Codex reports tools and replies in the same JSONL stream. Keep only the
// latest assistant message, even when an earlier tool event is very large.
type codexOutput struct {
	line   cliBuffer
	text   string
	err    string
	failed bool
}

func (o *codexOutput) Write(p []byte) (int, error) {
	for rest := p; len(rest) > 0; {
		line, tail, more := bytes.Cut(rest, []byte{'\n'})
		_, _ = o.line.Write(line)
		if more {
			o.consumeLine()
		}
		rest = tail
	}
	return len(p), nil
}

func (o *codexOutput) consumeLine() {
	var e struct {
		Type, Message string
		Item          struct{ Type, Text string }
		Error         struct{ Message string }
	}
	if json.Unmarshal(o.line.Bytes(), &e) == nil {
		switch {
		case e.Type == "item.completed" && e.Item.Type == "agent_message":
			o.text = strings.TrimSpace(e.Item.Text)
		case e.Type == "turn.failed":
			o.failed = true
			if e.Error.Message != "" {
				o.err = e.Error.Message
			}
		case e.Type == "error":
			o.err = e.Message
		}
	}
	o.line.Reset()
}
