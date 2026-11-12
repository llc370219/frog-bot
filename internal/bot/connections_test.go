package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const cliTestPrompt = "测试 CLI 参数：引号 \" 和 & | %PATH% $(echo no)"
const cliTestReply = "CLI 已收到完整任务"

// Reuse the test executable as a local CLI fixture. No real credentials or
// vendor service are needed to exercise the HTTP-to-process path on Windows.
func TestMain(m *testing.M) {
	id := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if os.Getenv("FROGBOT_CLI_TEST") == "1" && slices.Contains([]string{"codex", "claude", "pi", "grok", "agy"}, id) {
		input, err := io.ReadAll(os.Stdin)
		if err != nil || !strings.Contains(string(input)+strings.Join(os.Args[1:], " "), cliTestPrompt) {
			fmt.Fprintln(os.Stderr, "prompt missing or altered")
			os.Exit(1)
		}
		if _, err := os.Stat("cli-workspace"); err != nil {
			fmt.Fprintln(os.Stderr, "wrong workspace")
			os.Exit(1)
		}
		switch id {
		case "codex":
			fmt.Println(`{"type":"item.completed","item":{"type":"command_execution","text":"ignore this"}}`)
			fmt.Printf("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":%q}}\n", cliTestReply)
		case "agy":
			fmt.Printf("{\"status\":\"SUCCESS\",\"response\":%q}\n", cliTestReply)
		default:
			fmt.Println(cliTestReply)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestAgentConnectionsExecuteOnApprovalAndSurviveRestart(t *testing.T) {
	dir, path := t.TempDir(), t.TempDir()
	t.Setenv("PATH", path)
	t.Setenv("HOME", t.TempDir()) // CLI 也会在家目录里找，测试不能看到本机真实装的 CLI
	t.Setenv("NVM_DIR", "")
	t.Setenv("FROGBOT_CLI_TEST", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"codex", "claude", "pi", "grok", "agy"}
	for _, id := range ids {
		name := id
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(path, name), image, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace")}
	open := func() *Bots {
		t.Helper()
		bs, err := OpenBots(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(bs.Close)
		return bs
	}
	bs := open()
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "cli-workspace"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	request := func(method, url, body string, code int) string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		bs.Handler(nil).ServeHTTP(w, r)
		if w.Code != code {
			t.Fatalf("%s %s: got %d, want %d: %s", method, url, w.Code, code, w.Body)
		}
		return w.Body.String()
	}
	const settings = "/api/settings/connections"
	var detected connectionsView
	if raw := request("GET", settings, "", http.StatusOK); json.Unmarshal([]byte(raw), &detected) != nil ||
		detected.CLI != "" || len(detected.CLIs) != len(ids) {
		t.Fatalf("invalid detection result: %s", raw)
	}
	for _, cli := range detected.CLIs {
		if !cli.Installed {
			t.Fatalf("%s fixture not detected", cli.ID)
		}
	}
	request("POST", settings, `{"cli":"powershell"}`, http.StatusBadRequest)
	request("POST", settings, `{}`, http.StatusBadRequest)
	const botPath = "/api/bots/main"
	run := func(cli, decision, status string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"text": cliTestPrompt})
		request("POST", botPath+"/messages", string(payload), http.StatusOK)
		var task TaskRec
		var approval string
		waitFor(t, "CLI approval", func() bool {
			tasks := bs.main.store.Tasks()
			if len(tasks) == 0 {
				return false
			}
			task = tasks[len(tasks)-1]
			for _, req := range bs.main.approver.Pending() {
				if req.TaskID == task.ID {
					approval = req.ID
					return true
				}
			}
			return false
		})
		if task.CLI != cli || task.Output != "" || task.Status != "running" {
			t.Fatalf("wrong route or ran before approval: %+v", task)
		}
		request("POST", botPath+"/approvals/"+approval, fmt.Sprintf(`{"decision":%q}`, decision), http.StatusOK)
		waitFor(t, "CLI to finish", func() bool {
			task, _ = bs.main.store.Task(task.ID)
			return terminal(task.Status)
		})
		if task.Status != status || status == "completed" && task.Output != cliTestReply {
			t.Fatalf("unexpected CLI result: %+v", task)
		}
	}
	request("POST", settings, `{"cli":"codex"}`, http.StatusOK)
	run("codex", "deny", "failed")
	for _, id := range ids {
		request("POST", settings, fmt.Sprintf(`{"cli":%q}`, id), http.StatusOK)
		run(id, "once", "completed")
	}
	bs.Close()
	bs = open()
	if bs.main.selectedCLI() != "agy" {
		t.Fatal("selection lost on restart")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // CLI 也会在家目录里找，测试不能看到本机真实装的 CLI
	t.Setenv("NVM_DIR", "")
	request("POST", settings, `{"cli":"pi"}`, http.StatusBadRequest)
	request("POST", settings, `{"cli":""}`, http.StatusOK)
	if bs.main.selectedCLI() != "" {
		t.Fatal("could not switch back to built-in Agent")
	}
}
