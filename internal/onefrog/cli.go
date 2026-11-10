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
	"runtime"
	"strings"
	"time"
)

const CLITool = "run_agent_cli"

type CLIStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
}

var cliNames = []CLIStatus{
	{ID: "codex", Name: "Codex CLI"},
	{ID: "claude", Name: "Claude Code"},
	{ID: "pi", Name: "Pi"},
	{ID: "grok", Name: "Grok CLI"},
	{ID: "agy", Name: "Antigravity CLI"},
}

func CLIName(id string) string {
	for _, cli := range cliNames {
		if cli.ID == id {
			return cli.Name
		}
	}
	return ""
}

func DetectCLIs() []CLIStatus {
	status := append([]CLIStatus(nil), cliNames...)
	for i := range status {
		_, _, err := resolveCLI(status[i].ID)
		status[i].Installed = err == nil
	}
	return status
}

func resolveCLI(id string) (string, []string, error) {
	if CLIName(id) == "" {
		return "", nil, fmt.Errorf("不支持的 Agent CLI：%s", id)
	}
	if runtime.GOOS != "windows" {
		path, err := exec.LookPath(id)
		return path, nil, err
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

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func cliArgs(id, prompt string) (args []string, stdin string) {
	switch id {
	case "codex":
		return []string{"exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "--json", "-"}, prompt
	case "claude":
		return []string{"-p", "--output-format", "text", "请根据标准输入中的任务说明执行并回复。"}, prompt
	case "pi":
		return []string{"--print", "--no-session"}, prompt
	case "grok":
		return []string{"--no-auto-update", "--output-format", "plain", "-p", prompt}, ""
	case "agy":
		return []string{"--output-format", "json", "-p", prompt}, ""
	default:
		return nil, ""
	}
}

func runCLI(ctx context.Context, id, workspace, prompt string) (string, error) {
	name := CLIName(id)
	exe, prefix, err := resolveCLI(id)
	if err != nil {
		return "", fmt.Errorf("找不到 %s。请在 Frog Bot 所在机器安装 CLI 并加入 PATH", name)
	}
	args, stdin := cliArgs(id, prompt)
	if stdin == "" && len(prompt) > 24_000 {
		return "", fmt.Errorf("%s 的任务说明过长，请缩短后重试", name)
	}
	cmd := exec.CommandContext(ctx, exe, append(prefix, args...)...)
	cmd.Dir = workspace
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	// Keep the CLI's own authentication, but don't pass Frog Bot's credentials.
	for _, kv := range cmd.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(key) {
		case "FROGBOT_MASTER_KEY", "FROGBOT_API_KEY", "FROGBOT_PASSWORD", "API_KEY":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
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
