package agy

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizePathMatchKeyWindowsAndURI(t *testing.T) {
	cases := []struct {
		in   string
		win  bool
		want string
	}{
		{`C:\Users\llc37\Desktop\grouting`, true, `c:/users/llc37/desktop/grouting`},
		{`c:/users/llc37/desktop/grouting/`, true, `c:/users/llc37/desktop/grouting`},
		{`\\?\C:\Users\llc37\Desktop\grouting`, true, `c:/users/llc37/desktop/grouting`},
		{`\\?\c:\Users\llc37\Desktop\grouting\`, false, `c:/users/llc37/desktop/grouting`},
		{`file:///c:/Users/llc37/Desktop/grouting`, true, `c:/users/llc37/desktop/grouting`},
		{`file:///C%3A/Users/llc37/Desktop/grouting/`, false, `c:/users/llc37/desktop/grouting`},
		{`\\?\UNC\server\share\repo\`, true, `//server/share/repo`},
		{`file:///Users/ll/frog-bot/`, false, `/Users/ll/frog-bot`},
		{`/Users/ll/frog-bot`, false, `/Users/ll/frog-bot`},
	}
	for _, tc := range cases {
		got := normalizePathMatchKey(tc.in, tc.win)
		if got != tc.want {
			t.Errorf("normalizePathMatchKey(%q, win=%v) = %q, want %q", tc.in, tc.win, got, tc.want)
		}
	}
}

func TestResolveProjectForCwdExactAndSubdir(t *testing.T) {
	projs := []projMatchEntry{
		{id: "p_root", key: "c:/users/llc37/desktop"},
		{id: "p_grout", key: "c:/users/llc37/desktop/grouting"},
		{id: "p_other", key: "c:/users/llc37/other"},
	}
	if got := resolveProjectForCwd("c:/users/llc37/desktop/grouting", projs); got != "p_grout" {
		t.Fatalf("exact match got %q, want p_grout", got)
	}
	if got := resolveProjectForCwd("c:/users/llc37/desktop/grouting/src/calc", projs); got != "p_grout" {
		t.Fatalf("subdir most-specific match got %q, want p_grout", got)
	}
	if got := resolveProjectForCwd("c:/users/llc37/desktop/grouting-v2", projs); got != "p_root" {
		t.Fatalf("prefix boundary mismatch got %q, want p_root (not p_grout)", got)
	}
	if got := resolveProjectForCwd("d:/unrelated", projs); got != "" {
		t.Fatalf("unrelated path got %q, want empty", got)
	}
}

func TestNativeClaudeAndCodexDiscoveryHistoryAndResume(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "grouting")
	subDir := filepath.Join(projDir, "backend")
	otherDir := filepath.Join(root, "other")
	for _, d := range []string{projDir, subDir, otherDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// 1) 构造 Claude Code 原生 ~/.claude/projects/<encoded>/<uuid>.jsonl（含 file-history-snapshot、meta、tool_use/tool_result、custom-title、sidechain）
	claudeCfg := filepath.Join(root, "claude-cfg")
	claudeProj := filepath.Join(claudeCfg, "projects", "-tmp-grouting")
	if err := os.MkdirAll(filepath.Join(claudeProj, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeUUID := "11111111-2222-3333-4444-555555555555"
	claudeJSONL := strings.Join([]string{
		`{"type":"file-history-snapshot","snapshot":{}}`,
		fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"timestamp":"2026-11-27T10:00:00Z","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>hidden</local-command-caveat>"}}`, claudeUUID, projDir),
		fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"timestamp":"2026-11-27T10:00:01Z","message":{"role":"user","content":"请检查 grouting 注浆公式"}}`, claudeUUID, projDir),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"cwd":%q,"timestamp":"2026-11-27T10:00:02Z","message":{"model":"<synthetic>","content":[{"type":"thinking","thinking":"先查看公式文件"}]}}`, claudeUUID, projDir),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"cwd":%q,"timestamp":"2026-11-27T10:00:03Z","message":{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"tu_9","name":"Read","input":{"file_path":"calc.py"}}]}}`, claudeUUID, projDir),
		fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"timestamp":"2026-11-27T10:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_9","content":"def grout(): pass"}]}}`, claudeUUID, projDir),
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"cwd":%q,"timestamp":"2026-11-27T10:00:05Z","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"公式检查完成，已发现单位换算问题。"}]}}`, claudeUUID, projDir),
		fmt.Sprintf(`{"type":"custom-title","sessionId":%q,"customTitle":"注浆公式审查"}`, claudeUUID),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(claudeProj, claudeUUID+".jsonl"), []byte(claudeJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	// 子代理 agent-*.jsonl 和 subagents/ 下的文件不应作为独立会话暴露
	if err := os.WriteFile(filepath.Join(claudeProj, "agent-sub1.jsonl"), []byte(claudeJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeProj, "subagents", "22222222-2222-3333-4444-555555555555.jsonl"), []byte(claudeJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	// 另一个项目的 Claude 会话，不应出现在 grouting 下
	otherClaudeUUID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	otherClaudeJSONL := fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"timestamp":"2026-11-27T09:00:00Z","message":{"role":"user","content":"其他项目问题"}}`+"\n", otherClaudeUUID, otherDir)
	if err := os.WriteFile(filepath.Join(claudeProj, otherClaudeUUID+".jsonl"), []byte(otherClaudeJSONL), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2) 构造 Codex 原生 ~/.codex/sessions/2026/11/27/rollout-*.jsonl（含子目录匹配、boilerplate 过滤、IDE 包装、reasoning+tool_call、subagent 跳过、全局标题）
	codexHome := filepath.Join(root, "codex-home")
	codexDay := filepath.Join(codexHome, "sessions", "2026", "11", "27")
	if err := os.MkdirAll(codexDay, 0o755); err != nil {
		t.Fatal(err)
	}
	codexUUID := "019a1b2c-3d4e-7f00-8000-abcdef123456"
	codexJSONL := strings.Join([]string{
		fmt.Sprintf(`{"timestamp":"2026-11-27T11:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"source":"vscode"}}`, codexUUID, subDir),
		fmt.Sprintf(`{"timestamp":"2026-11-27T11:00:01Z","type":"turn_context","payload":{"cwd":%q,"model":"gpt-5.4"}}`, subDir),
		`{"timestamp":"2026-11-27T11:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for grouting"}]}}`,
		`{"timestamp":"2026-11-27T11:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context><cwd>/tmp</cwd></environment_context>"}]}}`,
		`{"timestamp":"2026-11-27T11:00:04Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# Context from my IDE setup:\nOpen files: main.py\n\n## My request for Codex:\n帮我优化注浆数据导出速度"}]}}`,
		`{"timestamp":"2026-11-27T11:00:05Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"分析导出瓶颈"}]}}`,
		`{"timestamp":"2026-11-27T11:00:06Z","type":"response_item","payload":{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pytest -q\"}"}}`,
		`{"timestamp":"2026-11-27T11:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"12 passed in 0.4s"}}`,
		`{"timestamp":"2026-11-27T11:00:08Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"已改为批量流式写入，导出速度提升 5 倍。"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(codexDay, "rollout-2026-11-27T11-00-00-"+codexUUID+".jsonl"), []byte(codexJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	// 子代理 Codex 线程必须被跳过
	subCodexJSONL := fmt.Sprintf(`{"timestamp":"2026-11-27T11:05:00Z","type":"session_meta","payload":{"id":"sub-thread-1","cwd":%q,"parent_thread_id":%q,"source":{"subagent":"worker"}}}`+"\n"+
		`{"timestamp":"2026-11-27T11:05:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"子代理内部任务"}]}}`+"\n", projDir, codexUUID)
	if err := os.WriteFile(filepath.Join(codexDay, "rollout-sub-thread-1.jsonl"), []byte(subCodexJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	// 全局标题映射
	globalState := fmt.Sprintf(`{"thread-titles":{"titles":{%q:"Codex 导出性能优化"}}}`, codexUUID)
	if err := os.WriteFile(filepath.Join(codexHome, ".codex-global-state.json"), []byte(globalState), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", claudeCfg)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FROGBOT_AGY_BIN", filepath.Join(root, "no-agy"))
	t.Setenv("FROGBOT_CLAUDE_BIN", filepath.Join(root, "no-claude"))
	t.Setenv("FROGBOT_CODEX_BIN", filepath.Join(root, "no-codex"))

	hub, err := NewHub(Config{
		DataDir:      filepath.Join(root, "data"),
		ProjectsRoot: filepath.Join(root, "projects"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	proj, err := hub.CreateProject("grouting", projDir)
	if err != nil {
		t.Fatal(err)
	}

	// 3) ListSessions 应自动发现 grouting 下的 Claude Code + Codex 原生历史会话（含子目录 backend），并过滤其他项目和子代理
	list, err := hub.ListSessions(context.Background(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 native sessions for grouting, got %d: %+v", len(list), list)
	}

	byID := map[string]SessionSummary{}
	for _, s := range list {
		byID[s.ID] = s
	}

	cs, ok := byID["claude_"+claudeUUID]
	if !ok {
		t.Fatalf("missing discovered Claude session: %+v", list)
	}
	if cs.Agent != "claude" || cs.Title != "注浆公式审查" || cs.Model != "claude-sonnet-4-6" {
		t.Fatalf("unexpected Claude summary: %+v", cs)
	}

	xs, ok := byID["codex_"+codexUUID]
	if !ok {
		t.Fatalf("missing discovered Codex session: %+v", list)
	}
	if xs.Agent != "codex" || xs.Title != "Codex 导出性能优化" || xs.Model != "gpt-5.4" {
		t.Fatalf("unexpected Codex summary: %+v", xs)
	}

	// 4) 打开 Claude 原生会话快照，完整读取历史消息（含思考链、工具调用与工具输出，过滤 meta/tool_result 伪用户消息）
	cSnap, _, cUnsub, err := hub.Subscribe(context.Background(), cs.ID, proj.ID)
	if err != nil {
		t.Fatalf("Subscribe native Claude session: %v", err)
	}
	cUnsub()
	if len(cSnap.Messages) != 2 {
		t.Fatalf("want 2 messages in Claude native history, got %d: %+v", len(cSnap.Messages), cSnap.Messages)
	}
	if cSnap.Messages[0].Role != "user" || cSnap.Messages[0].Text != "请检查 grouting 注浆公式" {
		t.Fatalf("unexpected Claude user msg: %+v", cSnap.Messages[0])
	}
	if cSnap.Messages[1].Role != "assistant" ||
		!strings.Contains(cSnap.Messages[1].Text, "单位换算问题") ||
		!strings.Contains(cSnap.Messages[1].Thinking, "先查看公式文件") ||
		len(cSnap.Messages[1].Tools) != 1 ||
		cSnap.Messages[1].Tools[0].Detail != "calc.py" ||
		!strings.Contains(cSnap.Messages[1].Tools[0].Output, "def grout") {
		t.Fatalf("unexpected Claude assistant msg: %+v", cSnap.Messages[1])
	}

	// 5) 即使未提前调用 ListSessions（新 Hub 模拟服务重启后直接打开历史会话），也能按 nativeID 定位并读取 Codex 完整历史
	hub2, err := NewHub(Config{
		DataDir:      filepath.Join(root, "data"),
		ProjectsRoot: filepath.Join(root, "projects"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub2.Close()

	xSnap, _, xUnsub, err := hub2.Subscribe(context.Background(), xs.ID, proj.ID)
	if err != nil {
		t.Fatalf("Subscribe native Codex session directly: %v", err)
	}
	xUnsub()
	if len(xSnap.Messages) != 2 {
		t.Fatalf("want 2 messages in Codex native history, got %d: %+v", len(xSnap.Messages), xSnap.Messages)
	}
	if xSnap.Messages[0].Role != "user" || xSnap.Messages[0].Text != "帮我优化注浆数据导出速度" {
		t.Fatalf("unexpected Codex user msg (boilerplate/IDE unwrap failed): %+v", xSnap.Messages[0])
	}
	if xSnap.Messages[1].Role != "assistant" ||
		!strings.Contains(xSnap.Messages[1].Text, "提升 5 倍") ||
		!strings.Contains(xSnap.Messages[1].Thinking, "分析导出瓶颈") ||
		len(xSnap.Messages[1].Tools) != 1 ||
		xSnap.Messages[1].Tools[0].Detail != "pytest -q" ||
		!strings.Contains(xSnap.Messages[1].Tools[0].Output, "12 passed") {
		t.Fatalf("unexpected Codex assistant msg: %+v", xSnap.Messages[1])
	}

	// 6) 删除原生会话后只在 Frog Bot 内隐藏，不删除宿主机原 JSONL 文件
	if err := hub.DeleteSession(context.Background(), cs.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(claudeProj, claudeUUID+".jsonl")); err != nil {
		t.Fatalf("deleting session in Frog Bot must not remove native CLI jsonl: %v", err)
	}
	listAfterDel, err := hub.ListSessions(context.Background(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listAfterDel) != 1 || listAfterDel[0].ID != xs.ID {
		t.Fatalf("deleted Claude session should stay hidden, got %+v", listAfterDel)
	}

	// 7) 验证 Frog Bot 自己创建的 Claude 会话在获得 nativeID 后不会与磁盘 JSONL 重复列出
	hub.mu.Lock()
	hub.meta["claude_localshort1234"] = savedSessionMeta{
		ID:        "claude_localshort1234",
		ProjectID: proj.ID,
		Cwd:       projDir,
		Agent:     "claude",
		NativeID:  claudeUUID,
		Title:     "已绑定的本地会话",
		UpdatedAt: time.Now(),
	}
	delete(hub.meta, cs.ID)
	hub.mu.Unlock()

	listDedup, err := hub.ListSessions(context.Background(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	claudeCount := 0
	for _, s := range listDedup {
		if s.Agent == "claude" {
			claudeCount++
			if s.ID != "claude_localshort1234" {
				t.Fatalf("expected native JSONL to deduplicate onto existing meta ID, got %+v", s)
			}
		}
	}
	if claudeCount != 1 {
		t.Fatalf("expected exactly 1 deduplicated Claude session, got %d: %+v", claudeCount, listDedup)
	}
}

func TestACPListSessionsUnfilteredAndPaginated(t *testing.T) {
	// 启动一个专门测试分页和拒绝 cwd 过滤的假 ACP 服务器脚本
	root := t.TempDir()
	projDir := filepath.Join(root, "grouting")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realProjDir := realOrClean(projDir)

	script := filepath.Join(root, "fake-agy-paged.sh")
	body := fmt.Sprintf(`#!/bin/sh
exec python3 -u -c '
import sys, json
proj = %q
for line in sys.stdin:
    req = json.loads(line)
    m = req.get("method")
    rid = req.get("id")
    params = req.get("params") or {}
    if m == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"protocolVersion":1,"agentInfo":{"name":"FakeAgy","version":"2.0"}}}))
    elif m == "session/list":
        # 故意模拟：如果调用方传了 cwd 参数就漏掉会话，只有不带 cwd 时才返回全部并分页
        if "cwd" in params:
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"sessions":[]}}))
        elif not params.get("cursor"):
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{
                "sessions":[{"sessionId":"agy_p1","cwd":"file://" + proj,"title":"分页会话1","updatedAt":"2026-11-27T12:00:00Z"}],
                "nextCursor":"page2"
            }}))
        else:
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{
                "sessions":[{"sessionId":"agy_p2","cwd":proj + "/sub","title":"分页会话2","updatedAt":"2026-11-27T12:05:00Z"}]
            }}))
    elif m == "session/load":
        sid = params.get("sessionId")
        print(json.dumps({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":sid,"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"来自原机 agy"}}}}))
        print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"sessionId":sid}}))
'
`, realProjDir)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FROGBOT_AGY_BIN", script)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "empty-claude"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "empty-codex"))

	hub, err := NewHub(Config{
		DataDir:      filepath.Join(root, "data"),
		ProjectsRoot: filepath.Join(root, "projects"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	proj, err := hub.CreateProject("grouting", projDir)
	if err != nil {
		t.Fatal(err)
	}

	list, err := hub.ListSessions(context.Background(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 paginated agy sessions (URI + subdir), got %d: %+v", len(list), list)
	}
	if list[0].ID != "agy_p2" || list[1].ID != "agy_p1" {
		t.Fatalf("unexpected order/IDs: %+v", list)
	}

	// 确认打开带 file:// URI 原始 cwd 的会话也能成功 session/load
	snap, _, unsub, err := hub.Subscribe(context.Background(), "agy_p1", proj.ID)
	if err != nil {
		t.Fatalf("Subscribe agy_p1: %v", err)
	}
	unsub()
	if len(snap.Messages) != 1 || snap.Messages[0].Text != "来自原机 agy" {
		t.Fatalf("unexpected agy_p1 replay: %+v", snap.Messages)
	}
}

func TestReadJSONLLineSkipsOversizedLineCleanly(t *testing.T) {
	big := strings.Repeat("x", 1024)
	input := "{\"a\":1}\n" + big + "\n{\"b\":2}\n"
	br := bufio.NewReader(strings.NewReader(input))

	l1, s1, e1 := readJSONLLine(br, 256)
	if e1 != nil || s1 || string(l1) != `{"a":1}` {
		t.Fatalf("line 1 mismatch: line=%q skipped=%v err=%v", l1, s1, e1)
	}
	_, s2, e2 := readJSONLLine(br, 256)
	if e2 != nil || !s2 {
		t.Fatalf("line 2 should be skipped cleanly: skipped=%v err=%v", s2, e2)
	}
	l3, s3, e3 := readJSONLLine(br, 256)
	if e3 != nil || s3 || string(l3) != `{"b":2}` {
		t.Fatalf("line 3 mismatch after oversized line: line=%q skipped=%v err=%v", l3, s3, e3)
	}
}

func TestNativeClaudeAndCodexFollowUpUsesResume(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "grouting")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 原生 Claude JSONL
	claudeUUID := "33333333-4444-5555-6666-777777777777"
	claudeCfg := filepath.Join(root, "claude-cfg")
	claudeProj := filepath.Join(claudeCfg, "projects", "-tmp-grouting")
	if err := os.MkdirAll(claudeProj, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeLine := fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"timestamp":"2026-11-27T10:00:00Z","message":{"role":"user","content":"旧对话"}}`+"\n", claudeUUID, projDir)
	if err := os.WriteFile(filepath.Join(claudeProj, claudeUUID+".jsonl"), []byte(claudeLine), 0o600); err != nil {
		t.Fatal(err)
	}

	// 原生 Codex JSONL
	codexUUID := "019a9999-aaaa-7bbb-8ccc-111122223333"
	codexHome := filepath.Join(root, "codex-home")
	codexDay := filepath.Join(codexHome, "sessions", "2026", "11", "27")
	if err := os.MkdirAll(codexDay, 0o755); err != nil {
		t.Fatal(err)
	}
	codexLines := fmt.Sprintf(`{"timestamp":"2026-11-27T11:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"source":"cli"}}`+"\n"+
		`{"timestamp":"2026-11-27T11:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Codex 旧问题"}]}}`+"\n", codexUUID, projDir)
	if err := os.WriteFile(filepath.Join(codexDay, "rollout-"+codexUUID+".jsonl"), []byte(codexLines), 0o600); err != nil {
		t.Fatal(err)
	}

	// 假 claude 脚本：记录是否传入 --resume <claudeUUID>
	claudeArgsLog := filepath.Join(root, "claude-args.txt")
	fakeClaude := filepath.Join(root, "fake-claude.sh")
	claudeScript := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %q
exec python3 -u -c '
import sys, json
for line in sys.stdin:
    d = json.loads(line)
    if d.get("type") == "user":
        print(json.dumps({"type":"result","subtype":"success","session_id":%q,"is_error":False,"result":"Claude 续接成功"}))
'
`, claudeArgsLog, claudeUUID)
	if err := os.WriteFile(fakeClaude, []byte(claudeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// 假 codex app-server 脚本：记录 thread/resume 收到的 threadId
	codexResumeLog := filepath.Join(root, "codex-resume.txt")
	fakeCodex := filepath.Join(root, "fake-codex.sh")
	codexScript := fmt.Sprintf(`#!/bin/sh
exec python3 -u -c '
import sys, json
log_path = %q
for line in sys.stdin:
    req = json.loads(line)
    m = req.get("method")
    rid = req.get("id")
    p = req.get("params") or {}
    if m == "initialize":
        print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"userAgent":"fake-codex"}}))
    elif m == "thread/resume":
        with open(log_path, "w") as fp:
            fp.write(p.get("threadId", ""))
        print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"thread":{"id":p.get("threadId", "")}}}))
    elif m == "turn/start":
        tid = p.get("threadId", "")
        print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"turn":{"id":"t1"}}}))
        print(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"threadId":tid,"delta":"Codex 续接成功"}}))
        print(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":tid,"turn":{"id":"t1","status":"completed"}}}))
'
`, codexResumeLog)
	if err := os.WriteFile(fakeCodex, []byte(codexScript), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", claudeCfg)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("FROGBOT_AGY_BIN", filepath.Join(root, "no-agy"))
	t.Setenv("FROGBOT_CLAUDE_BIN", fakeClaude)
	t.Setenv("FROGBOT_CODEX_BIN", fakeCodex)

	hub, err := NewHub(Config{
		DataDir:      filepath.Join(root, "data"),
		ProjectsRoot: filepath.Join(root, "projects"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	proj, err := hub.CreateProject("grouting", projDir)
	if err != nil {
		t.Fatal(err)
	}

	// 1) 续聊原生 Claude 会话
	cID := "claude_" + claudeUUID
	_, cLive, cUnsub, err := hub.Subscribe(context.Background(), cID, proj.ID)
	if err != nil {
		t.Fatalf("Subscribe claude: %v", err)
	}
	defer cUnsub()
	if _, err := hub.SendPrompt(context.Background(), cID, proj.ID, "继续分析", ""); err != nil {
		t.Fatalf("SendPrompt claude: %v", err)
	}
	waitTurnDone(t, cLive)

	rawArgs, err := os.ReadFile(claudeArgsLog)
	if err != nil {
		t.Fatalf("read claude args log: %v", err)
	}
	if !strings.Contains(string(rawArgs), "--resume\n"+claudeUUID) {
		t.Fatalf("expected claude --resume %s, got args:\n%s", claudeUUID, string(rawArgs))
	}

	// 2) 续聊原生 Codex 会话
	xID := "codex_" + codexUUID
	_, xLive, xUnsub, err := hub.Subscribe(context.Background(), xID, proj.ID)
	if err != nil {
		t.Fatalf("Subscribe codex: %v", err)
	}
	defer xUnsub()
	if _, err := hub.SendPrompt(context.Background(), xID, proj.ID, "继续 Codex", ""); err != nil {
		t.Fatalf("SendPrompt codex: %v", err)
	}
	waitTurnDone(t, xLive)

	rawResume, err := os.ReadFile(codexResumeLog)
	if err != nil {
		t.Fatalf("read codex resume log: %v", err)
	}
	if strings.TrimSpace(string(rawResume)) != codexUUID {
		t.Fatalf("expected codex thread/resume %s, got %q", codexUUID, string(rawResume))
	}
}
