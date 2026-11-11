package onefrog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var errCLIPaused = errors.New("CLI 已暂停")

func (a *Agent) runCLIStep(ctx context.Context, t Task, msgs []Message, em *emitter, pc *pauseCtl) (string, error) {
	name := CLIName(t.CLI)
	if _, _, err := resolveCLI(t.CLI); err != nil {
		return "", fmt.Errorf("找不到 %s。请在 Frog Bot 所在机器安装 CLI 并加入 PATH", name)
	}
	g := a.Guard
	if g == nil {
		g, _ = NewGuard(Rules{}, nil, nil)
	}
	var p strings.Builder
	fmt.Fprintf(&p, "%s\n\n", Truncate(msgs[0].Content, 4500))
	if len(msgs) > 2 {
		var prior strings.Builder
		for _, m := range msgs[1 : len(msgs)-1] {
			fmt.Fprintf(&prior, "[%s]\n%s\n", m.Role, Truncate(m.Content, 2200))
		}
		fmt.Fprintf(&p, "先前上下文：\n%s\n\n", Truncate(prior.String(), 5200))
	}
	fmt.Fprintf(&p, "当前任务：\n%s", msgs[len(msgs)-1].Content)
	prompt := RedactString(g.Secrets, p.String())
	args, _ := json.Marshal(map[string]string{
		"cli": name, "model": t.CLIOptions.Model, "task": Truncate(RedactString(g.Secrets, msgs[len(msgs)-1].Content), 3500),
		"prompt_hash": fmt.Sprintf("%x", sha256.Sum256([]byte(prompt))),
	})
	id := NewID("cli")
	em.emit(EvToolCall, map[string]any{"call_id": id, "tool": CLITool, "args": json.RawMessage(args)})
	result := func(message string, denied, failed bool) {
		em.emit(EvToolResult, map[string]any{"call_id": id, "tool": CLITool,
			"output": Truncate(message, 4000), "error": failed, "denied": denied})
	}
	tool := FuncTool{ToolName: CLITool}
	if v := g.Check(ctx, t, tool, args, em.emit); v.Decision != Allow {
		result(v.Reason, true, false)
		return "", errors.New(v.Reason)
	}
	// Pause at the boundary, not while the CLI might be writing files.
	if pc.isRequested() {
		result("启动前已暂停", false, false)
		return "", errCLIPaused
	}
	out, err := runCLI(ctx, t.CLI, t.Workspace, prompt, t.CLIOptions)
	if err != nil {
		msg := RedactString(g.Secrets, err.Error())
		result(msg, false, true)
		return "", errors.New(msg)
	}
	out = RedactString(g.Secrets, out)
	result(out, false, false)
	return out, nil
}
