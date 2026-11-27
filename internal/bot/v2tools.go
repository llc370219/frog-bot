package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"frogbot/internal/clipool"
	"frogbot/internal/memory"
	"frogbot/internal/onefrog"
	"frogbot/internal/security"
)

// v2Tools exposes all Frog Bot V2 capabilities to the Bot chat layer:
//   - memory_manage (entry-ID atomic USER.md / MEMORY.md with taint review)
//   - session_search (SQLite FTS5 trigram native Chinese history search)
//   - read_file_lines (workspace-jailed pointer file reader)
//   - skill_save / skill_load (evolvable SKILL.md library)
//   - dispatch_cli_task / cancel_cli_task (isolated Coding CLI Auto/Plan pool)
//   - list_peers_status / send_peer_message (FrogLink Mesh RAM heartbeats & taint-carrying dispatch)
func (b *Bot) v2Tools() []onefrog.Tool {
	mk := func(name, desc, schema string, fn func(ctx context.Context, raw json.RawMessage) (string, error)) onefrog.Tool {
		return onefrog.PassSecrets{FuncTool: onefrog.FuncTool{
			ToolName:   name,
			Desc:       desc,
			JSONSchema: schema,
			RiskFn:     safe,
			Fn: func(ctx context.Context, _ onefrog.ToolEnv, a json.RawMessage) (string, error) {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				out, err := fn(ctx, a)
				if err == nil && b.v2 != nil && b.v2.Hermes != nil && b.v2.Hermes.Pointer != nil &&
					name != "read_file_lines" && name != "skill_load" {
					out, _, _ = b.v2.Hermes.Pointer.Intercept(ctx, out)
				}
				return out, err
			},
		}}
	}

	enforceTaintedAction := func(ctx context.Context, action, summary string) error {
		if b.v2 == nil || b.v2.Taint == nil || !b.v2.Taint.IsTainted() {
			return nil
		}
		hr := b.hostRuntime()
		if hr == nil || hr.SecurityGW == nil {
			return nil
		}
		dec := hr.SecurityGW.Enforce(ctx, security.ReviewInput{
			BotID:         b.ID(),
			Action:        action,
			TriggerSource: "chat:" + b.ID(),
			Summary:       summary,
			Taint:         b.v2.Taint.Snapshot(),
		})
		if dec.Verdict != security.VerdictApprove {
			return fmt.Errorf("安全审查拦截（%s）：%s", dec.By, dec.Reason)
		}
		return nil
	}

	return []onefrog.Tool{
		mk("memory_manage",
			"条目级原子维护有界长期记忆块（USER.md ≤1400字 / MEMORY.md ≤2200字），支持 add|update|remove|list，不破坏当前会话 Prompt Cache。",
			`{"type":"object","properties":{"action":{"type":"string","enum":["add","update","remove","list"]},"target":{"type":"string","enum":["user","memory"]},"entry_id":{"type":"string"},"content":{"type":"string"}},"required":["action","target"]}`,
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				if b.v2 == nil || b.v2.Hermes == nil {
					return "", errors.New("Hermes 记忆模块未初始化")
				}
				v, err := readArgs[struct {
					Action  string `json:"action"`
					Target  string `json:"target"`
					EntryID string `json:"entry_id"`
					Content string `json:"content"`
				}](raw)
				if err != nil {
					return "", err
				}
				if strings.ToLower(v.Action) == "list" {
					bs := b.v2.Hermes.MemoryBlock
					if strings.ToLower(v.Target) == "user" {
						bs = b.v2.Hermes.UserBlock
					}
					entries, usage, err := bs.List()
					if err != nil {
						return "", err
					}
					return encodePrettyJSON(map[string]any{"usage": usage, "entries": entries}), nil
				}
				if err := enforceTaintedAction(ctx, "memory_manage", fmt.Sprintf("%s %s %s: %s", v.Action, v.Target, v.EntryID, v.Content)); err != nil {
					return "", err
				}
				res, err := b.v2.Hermes.ManageMemory(v.Action, v.Target, v.EntryID, v.Content)
				if err != nil {
					return "", err
				}
				return encodePrettyJSON(res), nil
			}),

		mk("session_search",
			"基于 SQLite FTS5 (trigram) 对归档的历史对话做中文子串全文检索。",
			`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`,
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				if b.v2 == nil || b.v2.Hermes == nil || b.v2.Hermes.History == nil {
					return b.searchHistory("", 5), nil
				}
				v, err := readArgs[struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}](raw)
				if err != nil {
					return "", err
				}
				results, err := b.v2.Hermes.History.Search(ctx, v.Query, v.Limit)
				if err != nil {
					return "", err
				}
				if len(results) == 0 {
					return "未在归档历史中检索到相关记录。", nil
				}
				return encodePrettyJSON(results), nil
			}),

		mk("read_file_lines",
			"按起止行号（1-indexed）局部查看工作区文件或指针化落盘的 tmp_*.txt 文件。",
			`{"type":"object","properties":{"path":{"type":"string"},"start":{"type":"integer"},"end":{"type":"integer"}},"required":["path"]}`,
			func(_ context.Context, raw json.RawMessage) (string, error) {
				v, err := readArgs[struct {
					Path  string `json:"path"`
					Start int    `json:"start"`
					End   int    `json:"end"`
				}](raw)
				if err != nil {
					return "", err
				}
				out, err := memory.ReadFileLines(b.cfg.Workspace, v.Path, v.Start, v.End)
				if err != nil {
					return "", err
				}
				return b.vault.Scrub(out), nil
			}),

		mk("skill_save",
			"将跑通的工作流或打卡脚本固化沉淀为 skills/<name>/SKILL.md，上下文平时仅保留一行索引。",
			`{"type":"object","properties":{"name":{"type":"string"},"description":{"type":"string"},"steps_markdown":{"type":"string"}},"required":["name","description","steps_markdown"]}`,
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				if b.v2 == nil || b.v2.Hermes == nil || b.v2.Hermes.Skills == nil {
					return "", errors.New("技能库未初始化")
				}
				v, err := readArgs[struct {
					Name          string `json:"name"`
					Description   string `json:"description"`
					StepsMarkdown string `json:"steps_markdown"`
				}](raw)
				if err != nil {
					return "", err
				}
				if err := enforceTaintedAction(ctx, "skill_save", fmt.Sprintf("保存技能 %s: %s", v.Name, v.Description)); err != nil {
					return "", err
				}
				sk, err := b.v2.Hermes.Skills.Save(v.Name, v.Description, v.StepsMarkdown)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("技能 %s 已保存至 skills/%s/SKILL.md（描述：%s）。", sk.Name, sk.Name, sk.Description), nil
			}),

		mk("skill_load",
			"按需展开指定技能 skills/<name>/SKILL.md 的完整执行步骤。",
			`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`,
			func(_ context.Context, raw json.RawMessage) (string, error) {
				if b.v2 == nil || b.v2.Hermes == nil || b.v2.Hermes.Skills == nil {
					return "", errors.New("技能库未初始化")
				}
				v, err := readArgs[struct {
					Name string `json:"name"`
				}](raw)
				if err != nil {
					return "", err
				}
				sk, err := b.v2.Hermes.Skills.Load(v.Name)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("# 技能 %s\n简介：%s\n\n%s", sk.Name, sk.Description, sk.StepsMarkdown), nil
			}),

		mk("dispatch_cli_task",
			"异步调度降权隔离的本机官方 Coding CLI 池（codex/claude/gemini 原生 auto 或 plan 模式）执行写代码、修脚本等重工任务。立即返回 task_id，中间流静默落盘，完成后回传 ≤800 字结论。",
			`{"type":"object","properties":{"mode":{"type":"string","enum":["plan","auto"]},"cli_preference":{"type":"string"},"task_prompt":{"type":"string"},"timeout":{"type":"integer"}},"required":["task_prompt"]}`,
			func(_ context.Context, raw json.RawMessage) (string, error) {
				hr := b.hostRuntime()
				if hr == nil || hr.CLIPool == nil {
					return "", errors.New("Host LocalCLIPool 未初始化")
				}
				v, err := readArgs[struct {
					Mode          string `json:"mode"`
					CLIPreference string `json:"cli_preference"`
					TaskPrompt    string `json:"task_prompt"`
					Timeout       int    `json:"timeout"`
				}](raw)
				if err != nil {
					return "", err
				}
				logsDir := filepath.Join(b.cfg.DataDir, "logs")
				if b.v2 != nil {
					logsDir = b.v2.Layout.LogsDir
				}
				sock := ""
				if hr.ExecBridge != nil {
					sock = hr.ExecBridge.SocketPath
				}
				tainted := b.v2 != nil && b.v2.Taint != nil && b.v2.Taint.IsTainted()
				st, err := hr.CLIPool.Dispatch(hr.ctx, clipool.CLITaskRequest{
					BotID:          b.ID(),
					Mode:           v.Mode,
					CLIPreference:  v.CLIPreference,
					TaskPrompt:     v.TaskPrompt,
					Workspace:      b.cfg.Workspace,
					LogsDir:        logsDir,
					TimeoutSec:     v.Timeout,
					Priority:       clipool.PriorityUser,
					ExecSocketPath: sock,
					Tainted:        tainted,
				})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已异步派单给 %s（%s 模式），任务 ID：%s。客户端可打开 CLI 抽屉实时查看日志，完成后会自动回调 ≤800 字结论。", st.CLI, st.Mode, st.ID), nil
			}),

		mk("cancel_cli_task",
			"立即强杀正在排队或运行中的 Coding CLI 子进程并释放工作区锁。",
			`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}`,
			func(_ context.Context, raw json.RawMessage) (string, error) {
				hr := b.hostRuntime()
				if hr == nil || hr.CLIPool == nil {
					return "", errors.New("Host LocalCLIPool 未初始化")
				}
				v, err := readArgs[struct {
					TaskID string `json:"task_id"`
				}](raw)
				if err != nil {
					return "", err
				}
				if err := hr.CLIPool.Cancel(v.TaskID); err != nil {
					return "", err
				}
				return "已中止 CLI 任务 " + v.TaskID, nil
			}),

		mk("list_peers_status",
			"按需查询 FrogLink Mesh 中各跨服节点的实时在线状态、隧道延迟与本机 CLI 额度水位（仅存于宿主内存，不破坏 Prompt Cache）。",
			`{"type":"object","properties":{}}`,
			func(_ context.Context, _ json.RawMessage) (string, error) {
				hr := b.hostRuntime()
				if hr == nil || hr.Mesh == nil {
					return "当前未启用 FrogLink Mesh。", nil
				}
				return encodePrettyJSON(map[string]any{
					"self_node":   hr.Mesh.Identity.NodeID,
					"self_quotas": hr.Quota.Snapshot(),
					"peers":       hr.Mesh.ListPeersStatus(),
				}), nil
			}),

		mk("send_peer_message",
			"向跨服务器 FrogLink Mesh 上的指定 Bot（格式 bot@node）发送端到端加密派单消息，自动携带当前会话的污点标记跨服传递。",
			`{"type":"object","properties":{"target":{"type":"string"},"intent":{"type":"string"},"payload":{"type":"string"},"need_reply":{"type":"boolean"}},"required":["target","payload"]}`,
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				hr := b.hostRuntime()
				if hr == nil || hr.Mesh == nil {
					return "", errors.New("FrogLink Mesh 未初始化")
				}
				v, err := readArgs[struct {
					Target    string `json:"target"`
					Intent    string `json:"intent"`
					Payload   string `json:"payload"`
					NeedReply bool   `json:"need_reply"`
				}](raw)
				if err != nil {
					return "", err
				}
				tainted := b.v2 != nil && b.v2.Taint != nil && b.v2.Taint.IsTainted()
				var taintSrcs []string
				if tainted {
					for _, s := range b.v2.Taint.Snapshot().Sources {
						taintSrcs = append(taintSrcs, s.Kind+":"+s.Origin)
					}
					if err := enforceTaintedAction(ctx, "send_peer_message", fmt.Sprintf("向 %s 派单: %s", v.Target, v.Intent)); err != nil {
						return "", err
					}
				}
				pm, err := hr.Mesh.SendPeerMessage(ctx, b.ID(), v.Target, v.Intent, v.Payload, v.NeedReply, tainted, taintSrcs)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已通过 FrogLink 端到端加密通道派发至 %s（消息 ID：%s，污点传递：%t）。", pm.ToBot, pm.ID, pm.Tainted), nil
			}),
	}
}
