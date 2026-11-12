package bot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"frogbot/internal/onefrog"
)

type peerTestModel func(context.Context, onefrog.ChatRequest, func(string)) (onefrog.Message, error)

func (f peerTestModel) Chat(ctx context.Context, req onefrog.ChatRequest, onText func(string)) (onefrog.Message, error) {
	return f(ctx, req, onText)
}

func TestDevicePairChatStreamAndCollaboration(t *testing.T) {
	first, second := httptest.NewUnstartedServer(nil), httptest.NewUnstartedServer(nil)
	const token = "test-token-for-local-device-pair-12345"
	releaseDelta := make(chan struct{})
	replyMeta := regexp.MustCompile(`来自 Bot ([a-z0-9-]+/main) 的消息；request_id=(req_[a-f0-9]{16})；reply_to=；需要回复=true`)
	model := peerTestModel(func(ctx context.Context, req onefrog.ChatRequest, delta func(string)) (onefrog.Message, error) {
		last := req.Messages[len(req.Messages)-1]
		if m := replyMeta.FindStringSubmatch(last.Content); last.Role == "user" && len(m) > 0 {
			return toolCall("message_agent", map[string]string{"to_bot_id": m[1], "reply_to": m[2], "message": "来自另一台设备的答案"}), nil
		}
		if strings.Contains(last.Content, "show live output") && delta != nil {
			delta("第一段")
			select {
			case <-releaseDelta:
			case <-ctx.Done():
				return onefrog.Message{}, ctx.Err()
			}
			delta("第二段")
			return onefrog.Message{Content: "第一段第二段"}, nil
		}
		if delta != nil {
			delta("已收到")
		}
		return onefrog.Message{Content: "已收到"}, nil
	})
	open := func(server, peer *httptest.Server, id, peerID string) *Bots {
		t.Helper()
		dir := t.TempDir()
		bs, err := OpenBots(Config{Name: id, DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "ws"),
			ChatProvider: model, ChatModel: "chat", Devices: &DeviceConfig{ID: id, Name: id, Listen: server.Listener.Addr().String(),
				Peers: []PeerConfig{{ID: peerID, Name: peerID, URL: "http://" + peer.Listener.Addr().String(), Token: token}}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(bs.Close)
		server.Config.Handler = bs.PeerHandler()
		server.Start()
		t.Cleanup(server.Close)
		return bs
	}
	a := open(first, second, "alpha", "beta")
	b := open(second, first, "beta", "alpha")
	ui := httptest.NewServer(LocalOnly(a.Handler(nil)))
	defer ui.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request := func(method, url, body string, auth bool, want int) *http.Response {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.Header.Set(deviceHeader, "alpha")
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := ui.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			resp.Body.Close()
			t.Fatalf("%s %s: got %d, want %d", method, url, resp.StatusCode, want)
		}
		return resp
	}
	reqClose := func(method, url, body string, auth bool, want int) {
		t.Helper()
		request(method, url, body, auth, want).Body.Close()
	}
	checkDirectory := func(online bool, bots int) {
		t.Helper()
		res := request("GET", ui.URL+"/api/devices", "", false, 200)
		defer res.Body.Close()
		var groups []deviceGroup
		if err := json.NewDecoder(res.Body).Decode(&groups); err != nil || len(groups) != 2 ||
			groups[0].ID != "local" || groups[1].ID != "beta" || groups[1].Online != online ||
			len(groups[1].Bots) != bots || groups[1].Bots[0].ID != "main" {
			t.Fatalf("unexpected device directory: %+v (%v)", groups, err)
		}
	}
	checkDirectory(true, 1)
	reqClose("GET", second.URL+peerPrefix+"/bots", "", false, 401)
	reqClose("POST", second.URL+peerPrefix+"/bots", `{"name":"Unauthorized"}`, false, 401)
	reqClose("POST", second.URL+peerPrefix+"/bots/main/approvals/id", `{"decision":"once"}`, true, 404)
	reqClose("POST", second.URL+"/api/bots/main/approvals/id", `{"decision":"once"}`, true, 404)
	base := ui.URL + "/api/devices/beta/bots/main"
	reqClose("POST", base+"/approvals/id", `{"decision":"once"}`, false, 404)
	reqClose("POST", base+"/secrets", `{"name":"EXAMPLE","value":"example"}`, false, 404)
	reqClose("POST", second.URL+peerPrefix+"/bots/main/secrets", `{"name":"EXAMPLE","value":"example"}`, true, 404)
	reqClose("GET", base+"/settings/connections", "", false, 200)
	reqClose("POST", base+"/messages", `{"text":"not sent","files":[{"path":"local.txt"}]}`, false, 400)
	reqClose("GET", base+"/info", "", false, 200)
	reqClose("GET", base+"/schedules", "", false, 200)

	res := request("GET", base+"/stream", "", false, 200)
	defer res.Body.Close()
	stream := bufio.NewScanner(res.Body)
	next := func(kind, content string) Event {
		t.Helper()
		for stream.Scan() {
			line, ok := strings.CutPrefix(stream.Text(), "data: ")
			var ev Event
			if ok && json.Unmarshal([]byte(line), &ev) == nil && ev.Kind == kind && strings.Contains(string(ev.Data), content) {
				return ev
			}
		}
		t.Fatalf("missing stream event %q %q: %v", kind, content, stream.Err())
		return Event{}
	}
	next("replay.done", "")
	reqClose("POST", base+"/messages", `{"text":"show live output"}`, false, 200)
	next("chat.delta", "第一段")
	close(releaseDelta)
	reply := next("message", "第一段第二段")
	if len(a.main.store.Messages()) != 0 || countMsgs(b.main.store, "user", "show live output") != 1 {
		t.Fatal("remote GUI message was not delivered only to the target device")
	}
	var assistant Message
	if err := json.Unmarshal(reply.Data, &assistant); err != nil || assistant.ID == "" {
		t.Fatalf("invalid assistant event: %s", reply.Data)
	}
	reqClose("POST", base+"/messages/"+assistant.ID+"/react", `{"emoji":"👍"}`, false, 200)
	next("message.reaction", "👍")
	heldID, err := b.main.CreateTask("target task details", "Target task", TaskOpts{Hold: true})
	if err != nil {
		t.Fatal(err)
	}
	next("task.created", "target task details")
	reqClose("POST", base+"/tasks/"+heldID+"/cancel", `{}`, false, 200)
	if task, _ := b.main.store.Task(heldID); task.Status != "cancelled" {
		t.Fatalf("remote control did not update target task: %+v", task)
	}
	b.main.store.Append("agent", onefrog.Event{TaskID: "task_local", Type: onefrog.EvApprovalRequested, Time: time.Now(),
		Data: onefrog.ApprovalRequest{ID: "appr_local", TaskID: "task_local", Args: json.RawMessage(`{"command":"local-command"}`)}}, false)
	ev := next("agent", "appr_local")
	if strings.Contains(string(ev.Data), "local-command") || strings.Contains(string(ev.Data), "args") {
		t.Fatalf("remote stream exposed local approval details: %s", ev.Data)
	}
	res.Body.Close()
	b.main.say("补发的新消息")
	reconnected := request("GET", base+"/stream?after="+strconv.FormatInt(reply.Seq, 10), "", false, 200)
	defer reconnected.Body.Close()
	stream = bufio.NewScanner(reconnected.Body)
	for stream.Scan() {
		line, ok := strings.CutPrefix(stream.Text(), "data: ")
		var ev Event
		if !ok || json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Seq > 0 && ev.Seq <= reply.Seq {
			t.Fatal("reconnection replayed events before the cursor")
		}
		if ev.Kind == "replay.done" {
			t.Fatal("reconnection missed the new message")
		}
		if strings.Contains(string(ev.Data), "补发的新消息") {
			break
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	reconnected.Body.Close()

	// A real multipart upload travels to the target workspace, and its
	// download is returned with the same-origin file safety headers.
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "peer-attachment.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "stored on beta")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	uploadReq, err := http.NewRequestWithContext(ctx, "POST", base+"/uploads", &upload)
	if err != nil {
		t.Fatal(err)
	}
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploaded, err := ui.Client().Do(uploadReq)
	if err != nil {
		t.Fatal(err)
	}
	var files []Attachment
	err = json.NewDecoder(uploaded.Body).Decode(&files)
	uploaded.Body.Close()
	if uploaded.StatusCode != 200 || err != nil || len(files) != 1 {
		t.Fatalf("upload failed: status=%d, files=%v, error=%v", uploaded.StatusCode, files, err)
	}
	file := request("GET", base+"/files?path="+url.QueryEscape(files[0].Path), "", false, 200)
	content, readErr := io.ReadAll(file.Body)
	file.Body.Close()
	if readErr != nil || string(content) != "stored on beta" || file.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("unexpected remote file: %q (%v)", content, readErr)
	}
	payload, _ := json.Marshal(map[string]any{"text": "attached remotely", "files": files})
	reqClose("POST", base+"/messages", string(payload), false, 200)
	waitFor(t, "attachment message handled on target", func() bool {
		return len(b.main.store.Unhandled()) == 0 && !b.main.chatBusy()
	})
	var attached Message
	for _, m := range b.main.store.Messages() {
		if m.Text == "attached remotely" {
			attached = m
		}
	}
	if len(attached.Files) != 1 || len(a.main.store.Messages()) != 0 {
		t.Fatal("attachment message did not reach only the target Bot")
	}
	reqClose("POST", base+"/messages/"+attached.ID+"/edit", `{"text":"edited remotely"}`, false, 200)
	waitFor(t, "remote edit applied", func() bool { return countMsgs(b.main.store, "user", "edited remotely") == 1 })

	if _, err := a.main.askAgent("beta/main", "跨设备提问"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "correlated peer reply", func() bool {
		for _, m := range a.main.store.Inbox(true) {
			if m.FromBotID == "beta/main" && m.ReplyTo != "" && m.Text == "来自另一台设备的答案" {
				return true
			}
		}
		return false
	})
	if _, err := b.main.messageAgent("alpha/main", "普通通知", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.main.messageAgent("alpha/main", "伪造回复", "req_0000000000000000"); err == nil {
		t.Fatal("accepted a reply to an unknown request")
	}
	created := request("POST", ui.URL+"/api/devices/beta/bots", `{"name":"Remote Reviewer","instructions":"only stored on beta"}`, false, 200)
	var profile BotProfile
	decodeErr := json.NewDecoder(created.Body).Decode(&profile)
	created.Body.Close()
	remote := b.localProfiles()
	if decodeErr != nil || profile.Name != "Remote Reviewer" || profile.Instructions != "" ||
		len(a.localProfiles()) != 1 || len(remote) != 2 || remote[1].ID != profile.ID || remote[1].Instructions != "only stored on beta" {
		t.Fatalf("new Bot not scoped to target: returned=%+v, target=%+v, error=%v", profile, remote, decodeErr)
	}
	checkDirectory(true, 2)

	// The host GUI controls the selected device's own persisted settings.
	settings := ui.URL + "/api/devices/beta/settings"
	reqClose("GET", second.URL+peerPrefix+"/settings", "", false, 401)
	reqClose("POST", second.URL+peerPrefix+"/settings/connections", `{"cli":""}`, false, 401)
	reqClose("POST", ui.URL+"/api/devices/beta/auth/password", `{}`, false, 404)
	cliPath := t.TempDir()
	t.Setenv("PATH", cliPath)
	t.Setenv("HOME", t.TempDir()) // CLI 也会在家目录里找，测试不能看到本机真实装的 CLI
	t.Setenv("NVM_DIR", "")
	cliName := "pi"
	if runtime.GOOS == "windows" {
		cliName += ".exe"
	}
	if err := os.WriteFile(filepath.Join(cliPath, cliName), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	detected := request("GET", settings+"/connections", "", false, 200)
	var connections connectionsView
	err = json.NewDecoder(detected.Body).Decode(&connections)
	detected.Body.Close()
	piFound := false
	for _, cli := range connections.CLIs {
		if cli.ID == "pi" {
			piFound = cli.Installed
		}
	}
	if err != nil || connections.CLI != "" || !piFound {
		t.Fatalf("remote CLI detection failed: %+v (%v)", connections, err)
	}
	// Pi 要先在目标设备上选 API 渠道和模型（渠道在下面才配置）
	reqClose("POST", settings+"/connections", `{"cli":"pi"}`, false, 400)

	const key = "test-key-only-for-beta"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"beta-model"}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	draft := mustJSON(map[string]string{"base_url": upstream.URL + "/v1", "model": "beta-model", "api_key": key})
	models := request("POST", settings+"/models", draft, false, 200)
	modelData, _ := io.ReadAll(models.Body)
	models.Body.Close()
	if !bytes.Contains(modelData, []byte("beta-model")) {
		t.Fatalf("remote model catalog missing result: %s", modelData)
	}
	reqClose("POST", settings+"/test", draft, false, 200)
	saved := request("POST", settings, draft, false, 200)
	savedData, _ := io.ReadAll(saved.Body)
	saved.Body.Close()
	loaded := request("GET", settings, "", false, 200)
	loadedData, _ := io.ReadAll(loaded.Body)
	loaded.Body.Close()
	stored, err := os.ReadFile(b.main.pool.path)
	if err != nil || b.main.GetSettings().Model != "beta-model" || !b.main.GetSettings().HasAPIKey || a.main.GetSettings().Model != "" ||
		bytes.Contains(stored, []byte(key)) || bytes.Contains(savedData, []byte(key)) || bytes.Contains(loadedData, []byte(key)) {
		t.Fatal("remote API configuration was misrouted or exposed the key")
	}
	reqClose("POST", settings+"/connections", `{"cli":"pi","cli_channel":"primary","cli_model":"beta-model"}`, false, 200)
	if b.main.selectedCLI() != "pi" || a.main.selectedCLI() != "" || b.main.cliOptions("pi").Channel != "primary" {
		t.Fatal("CLI change was applied to the wrong device")
	}
	reqClose("POST", settings+"/connections", `{"cli":""}`, false, 200)

	previous := releaseClient
	releaseClient = &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v7.8.9"}`))}, nil
	})}
	t.Cleanup(func() { releaseClient = previous })
	update := request("GET", ui.URL+"/api/devices/beta/update", "", false, 200)
	var status updateStatus
	err = json.NewDecoder(update.Body).Decode(&status)
	update.Body.Close()
	if err != nil || status.Current != currentVersion() || status.Latest != "v7.8.9" {
		t.Fatalf("remote update check did not return target version: %+v (%v)", status, err)
	}

	second.Close()
	checkDirectory(false, 2)
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
