package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain 让测试二进制在 FROGBOT_FAKE_AGY=1 时扮演一个标准 ACP stdio 服务器，
// 无需真实 Google 登录或网络即可在沙箱内端到端测试项目目录、会话管理、流式对话和权限回调。
func TestMain(m *testing.M) {
	if os.Getenv("FROGBOT_FAKE_AGY") == "1" {
		runFakeACPServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runFakeACPServer() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	type sess struct {
		id       string
		cwd      string
		mode     string
		model    string
		cancelMu sync.Mutex
		cancelCh chan struct{}
	}
	sessions := map[string]*sess{}
	seq := 0
	var sendMu sync.Mutex
	send := func(v any) {
		raw, _ := json.Marshal(v)
		sendMu.Lock()
		fmt.Println(string(raw))
		sendMu.Unlock()
	}
	var permRespCh chan string

	for sc.Scan() {
		var env rpcEnvelope
		if json.Unmarshal(sc.Bytes(), &env) != nil {
			continue
		}
		if env.Method == "" && len(env.ID) > 0 && permRespCh != nil {
			var r struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			_ = json.Unmarshal(env.Result, &r)
			permRespCh <- r.Outcome.OptionID
			continue
		}
		switch env.Method {
		case "initialize":
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      env.ID,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentInfo":       map[string]string{"name": "Fake Antigravity ACP", "version": "1.5.13-test"},
				},
			})
		case "session/new":
			var p struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(env.Params, &p)
			seq++
			sid := fmt.Sprintf("sess_%d", seq)
			sessions[sid] = &sess{id: sid, cwd: p.Cwd, mode: "default", model: "gemini-4-carbon-high"}
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      env.ID,
				"result": map[string]any{
					"sessionId": sid,
					"modes": map[string]any{
						"currentModeId": "default",
						"availableModes": []map[string]string{
							{"id": "auto", "name": "Auto"},
							{"id": "accept-edits", "name": "Accept edits"},
							{"id": "plan", "name": "Plan"},
							{"id": "default", "name": "Default"},
						},
					},
					"models": map[string]any{
						"currentModelId": "gemini-4-carbon-high",
						"availableModels": []map[string]string{
							{"modelId": "gemini-4-carbon-high", "name": "Gemini 4 Carbon (High)"},
							{"modelId": "claude-opus-5-5-high", "name": "Claude Opus 5.5 (High)"},
						},
					},
				},
			})
		case "session/list":
			var list []map[string]string
			for _, s := range sessions {
				list = append(list, map[string]string{
					"sessionId": s.id,
					"cwd":       s.cwd,
					"title":     "已有会话",
					"updatedAt": time.Now().UTC().Format(time.RFC3339),
				})
			}
			send(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": map[string]any{"sessions": list}})
		case "session/load":
			var p struct {
				SessionID string `json:"sessionId"`
				Cwd       string `json:"cwd"`
			}
			_ = json.Unmarshal(env.Params, &p)
			if sessions[p.SessionID] == nil {
				sessions[p.SessionID] = &sess{id: p.SessionID, cwd: p.Cwd, mode: "auto", model: "gemini-4-carbon-high"}
			}
			send(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "user_message_chunk",
						"content":       map[string]string{"type": "text", "text": "历史问题"},
					},
				},
			})
			send(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]string{"type": "text", "text": "历史回答"},
					},
				},
			})
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      env.ID,
				"result": map[string]any{
					"sessionId": p.SessionID,
					"modes":     map[string]any{"currentModeId": sessions[p.SessionID].mode},
					"models":    map[string]any{"currentModelId": sessions[p.SessionID].model},
				},
			})
		case "session/set_mode":
			var p struct {
				SessionID string `json:"sessionId"`
				ModeID    string `json:"modeId"`
			}
			_ = json.Unmarshal(env.Params, &p)
			if s := sessions[p.SessionID]; s != nil {
				s.mode = p.ModeID
			}
			send(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": map[string]any{}})
		case "session/set_model":
			var p struct {
				SessionID string `json:"sessionId"`
				ModelID   string `json:"modelId"`
			}
			_ = json.Unmarshal(env.Params, &p)
			if s := sessions[p.SessionID]; s != nil {
				s.model = p.ModelID
			}
			send(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": map[string]any{}})
		case "session/close":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(env.Params, &p)
			delete(sessions, p.SessionID)
			send(map[string]any{"jsonrpc": "2.0", "id": env.ID, "result": map[string]any{}})
		case "session/cancel":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(env.Params, &p)
			if s := sessions[p.SessionID]; s != nil {
				s.cancelMu.Lock()
				if s.cancelCh != nil {
					close(s.cancelCh)
					s.cancelCh = nil
				}
				s.cancelMu.Unlock()
			}
		case "session/prompt":
			var p struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(env.Params, &p)
			promptText := ""
			if len(p.Prompt) > 0 {
				promptText = p.Prompt[0].Text
			}
			s := sessions[p.SessionID]
			var cancelCh chan struct{}
			if s != nil {
				s.cancelMu.Lock()
				cancelCh = make(chan struct{})
				s.cancelCh = cancelCh
				s.cancelMu.Unlock()
			}
			go func(id json.RawMessage, sid, text string, abortCh chan struct{}) {
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sid,
						"update": map[string]any{
							"sessionUpdate": "available_commands_update",
							"availableCommands": []map[string]any{
								{"name": "compact", "description": "压缩上下文"},
								{"name": "plan", "description": "规划模式", "input": map[string]string{"hint": "任务目标"}},
							},
						},
					},
				})
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sid,
						"update": map[string]any{
							"sessionUpdate": "agent_thought_chunk",
							"content":       []map[string]string{{"type": "text", "text": "先分析项目结构…"}},
						},
					},
				})
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sid,
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"toolCallId":    "tc_1",
							"name":          "run_command",
							"title":         "go test ./...",
							"kind":          "execute",
							"status":        "in_progress",
							"rawInput":      map[string]string{"command": "go test ./..."},
						},
					},
				})
				if strings.Contains(text, "慢任务") && abortCh != nil {
					select {
					case <-abortCh:
						send(map[string]any{
							"jsonrpc": "2.0",
							"id":      id,
							"result":  map[string]any{"stopReason": "cancelled"},
						})
						return
					case <-time.After(1200 * time.Millisecond):
					}
				}
				chosen := "auto"
				if strings.Contains(text, "需要授权") {
					permRespCh = make(chan string, 1)
					send(map[string]any{
						"jsonrpc": "2.0",
						"id":      999,
						"method":  "session/request_permission",
						"params": map[string]any{
							"sessionId": sid,
							"toolCall": map[string]any{
								"toolCallId": "tc_perm",
								"name":       "run_command",
								"title":      "部署脚本 deploy.sh",
								"kind":       "execute",
								"args":       map[string]string{"CommandLine": "./deploy.sh"},
							},
							"options": []map[string]string{
								{"optionId": "allow_once", "name": "允许一次", "kind": "allow_once"},
								{"optionId": "reject_once", "name": "拒绝", "kind": "reject_once"},
							},
						},
					})
					chosen = <-permRespCh
				}
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sid,
						"update": map[string]any{
							"sessionUpdate": "tool_call_update",
							"toolCallId":    "tc_1",
							"status":        "completed",
							"rawOutput":     map[string]string{"output": "ok  frogbot/internal/agy"},
						},
					},
				})
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sid,
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]string{"type": "text", "text": "已完成：" + text + " (" + chosen + ")"},
						},
					},
				})
				send(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  map[string]any{"stopReason": "end_turn"},
				})
			}(env.ID, p.SessionID, promptText, cancelCh)
		}
	}
}

func TestAgyProjectsSessionsStreamingAndPermissions(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FROGBOT_AGY_BIN", exe)
	t.Setenv("FROGBOT_FAKE_AGY", "1")

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	projRoot := filepath.Join(root, "projects")
	externalRepo := filepath.Join(root, "external-repo")
	if err := os.MkdirAll(externalRepo, 0o755); err != nil {
		t.Fatal(err)
	}

	hub, err := NewHub(Config{DataDir: dataDir, ProjectsRoot: projRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	handler := hub.Handler()

	callHTTP := func(method, target, body string, wantCode int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != wantCode {
			t.Fatalf("%s %s: got %d, want %d: %s", method, target, rec.Code, wantCode, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	// 1) 状态检测
	var st StatusView
	_ = json.Unmarshal(callHTTP("GET", "/api/agy/status", "", 200), &st)
	if !st.Installed || st.AgentVersion != "1.5.13-test" {
		t.Fatalf("unexpected status: %+v", st)
	}

	// 2) 项目文件夹：新建托管目录、关联外部仓库、阻止把 frog 数据目录设为项目
	var created Project
	_ = json.Unmarshal(callHTTP("POST", "/api/agy/projects", `{"name":"demo-app"}`, 200), &created)
	if !created.Managed || filepath.Base(created.Path) != "demo-app" {
		t.Fatalf("managed project wrong: %+v", created)
	}
	var linked Project
	_ = json.Unmarshal(callHTTP("POST", "/api/agy/projects", fmt.Sprintf(`{"name":"外部仓库","path":%q}`, externalRepo), 200), &linked)
	if linked.Managed {
		t.Fatalf("linked external project should not be marked managed: %+v", linked)
	}
	callHTTP("POST", "/api/agy/projects", fmt.Sprintf(`{"name":"偷密钥","path":%q}`, dataDir), 400)
	callHTTP("POST", "/api/agy/projects", `{"name":"../escape"}`, 400)

	// 3) 在项目下新建对话并切换模式/模型
	var snap Snapshot
	_ = json.Unmarshal(callHTTP("POST", "/api/agy/sessions", fmt.Sprintf(`{"project_id":%q,"mode":"plan","model":"claude-opus-5-5-high"}`, created.ID), 200), &snap)
	if snap.Session.ID == "" || snap.Session.Mode != "plan" || snap.Session.Model != "claude-opus-5-5-high" {
		t.Fatalf("session create wrong: %+v", snap.Session)
	}
	sid := snap.Session.ID

	// 4) 订阅实时流，发送带 agy 权限确认的对话并点选「允许一次」
	_, live, unsub, err := hub.Subscribe(context.Background(), sid, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	callHTTP("POST", "/api/agy/sessions/"+sid+"/messages", `{"text":"部署前需要授权"}`, 200)

	var permID string
	deadline := time.After(5 * time.Second)
	for permID == "" {
		select {
		case ev := <-live:
			if ev.Kind == "permission.upsert" {
				raw, _ := json.Marshal(ev.Data)
				var pr PermissionReq
				_ = json.Unmarshal(raw, &pr)
				if pr.Status == "pending" {
					permID = pr.ID
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for agy permission request")
		}
	}

	callHTTP("POST", "/api/agy/sessions/"+sid+"/permissions/"+permID, `{"option_id":"allow_once"}`, 200)

	done := false
	for !done {
		select {
		case ev := <-live:
			if ev.Kind == "turn.done" {
				done = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn.done")
		}
	}

	// 检查消息、思考与工具调用（含 Detail/Output/Commands）均已落入会话快照
	snapNow, _, unsub2, err := hub.Subscribe(context.Background(), sid, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	unsub2()
	if len(snapNow.Messages) != 2 {
		t.Fatalf("want 2 messages, got %+v", snapNow.Messages)
	}
	asst := snapNow.Messages[1]
	if !strings.Contains(asst.Text, "(allow_once)") || !strings.Contains(asst.Thinking, "先分析项目结构") || len(asst.Tools) != 1 ||
		asst.Tools[0].Detail != "go test ./..." || !strings.Contains(asst.Tools[0].Output, "frogbot/internal/agy") ||
		len(snapNow.Commands) != 2 {
		t.Fatalf("unexpected assistant message or commands: msg=%+v cmds=%+v", asst, snapNow.Commands)
	}

	// 5) 忙碌时 follow_up 排队、撤回，以及 steer 打断插话
	callHTTP("POST", "/api/agy/sessions/"+sid+"/messages", `{"text":"慢任务执行中"}`, 200)
	var qRes1, qRes2 SendResult
	_ = json.Unmarshal(callHTTP("POST", "/api/agy/sessions/"+sid+"/messages", `{"text":"待撤回的追问","behavior":"follow_up"}`, 200), &qRes1)
	_ = json.Unmarshal(callHTTP("POST", "/api/agy/sessions/"+sid+"/messages", `{"text":"排队收尾","behavior":"follow_up"}`, 200), &qRes2)
	if !qRes1.Queued || qRes1.QueueItem == nil || !qRes2.Queued {
		t.Fatalf("expected follow_up messages to queue: q1=%+v q2=%+v", qRes1, qRes2)
	}
	callHTTP("DELETE", "/api/agy/sessions/"+sid+"/queue/"+qRes1.QueueItem.ID, "", 200)

	var steerRes SendResult
	_ = json.Unmarshal(callHTTP("POST", "/api/agy/sessions/"+sid+"/messages", `{"text":"立即改道","behavior":"steer"}`, 200), &steerRes)
	if !steerRes.Queued || steerRes.Behavior != "steer" {
		t.Fatalf("expected steer result: %+v", steerRes)
	}

	// 等 steer 和 follow_up 两条全部跑完
	steerDeadline := time.After(5 * time.Second)
	for {
		snapQ, _, unsubQ, err := hub.Subscribe(context.Background(), sid, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		unsubQ()
		if !snapQ.Session.Busy && len(snapQ.Queue) == 0 && len(snapQ.Messages) >= 7 {
			joined, _ := json.Marshal(snapQ.Messages)
			s := string(joined)
			if strings.Contains(s, "待撤回的追问") {
				t.Fatalf("cancelled queued item should not run: %s", s)
			}
			if !strings.Contains(s, "立即改道") || !strings.Contains(s, "排队收尾") {
				t.Fatalf("missing steer or follow_up results: %s", s)
			}
			break
		}
		select {
		case <-steerDeadline:
			t.Fatalf("timed out waiting for steer/follow_up completion: busy=%v msgs=%d", snapQ.Session.Busy, len(snapQ.Messages))
		case <-time.After(30 * time.Millisecond):
		}
	}

	// 6) 历史会话按 session/load 自动回放
	loadedSnap, _, unsub3, err := hub.Subscribe(context.Background(), "sess_historical", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	unsub3()
	if len(loadedSnap.Messages) != 2 || loadedSnap.Messages[0].Text != "历史问题" || loadedSnap.Messages[1].Text != "历史回答" {
		t.Fatalf("history replay failed: %+v", loadedSnap.Messages)
	}
}
