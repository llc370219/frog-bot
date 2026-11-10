package bot

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"frogbot/internal/auth"
)

// Test real authenticated HTTP using browser/WebView cookie jars and the native
// client's explicit Cookie header. No model or client UI is involved.
func TestClientsShareAuthenticatedMessagesAndResumeEvents(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	bs := &Bots{main: b, runtimes: map[string]*Bot{defaultBotID: b},
		handlers: map[string]*botEndpoint{defaultBotID: {Handler: b.Handler("")}}}
	if err := auth.SetPassword(b.cfg.DataDir, "admin", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	a, err := auth.Open(b.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Middleware(bs.Handler(nil)))
	defer srv.Close()

	request := func(client *http.Client, method, path, body, cookie string, want int) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		} else {
			req.Header.Set("Origin", srv.URL)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		if resp.StatusCode != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, resp.StatusCode, want)
		}
		return resp
	}
	// bcrypt is intentionally expensive, especially under the race detector.
	// Keep a bounded timeout without making instrumentation a login failure.
	native := &http.Client{Timeout: 30 * time.Second}
	request(native, "GET", "/api/health", "", "", 200).Body.Close()
	request(native, "GET", "/api/capabilities", "", "", 401).Body.Close()
	clients := []*http.Client{nil, nil, native}
	for i := 0; i < 2; i++ {
		jar, _ := cookiejar.New(nil)
		clients[i] = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	}
	cookie := ""
	for i, client := range clients {
		resp := request(client, "POST", "/api/auth/login", `{"username":"admin","password":"correct-horse"}`, "", 200)
		if i == 2 {
			for _, c := range resp.Cookies() {
				if c.Name == auth.CookieName {
					cookie = c.Name + "=" + c.Value
				}
			}
		}
		resp.Body.Close()
	}
	if cookie == "" {
		t.Fatal("native login did not return a session")
	}
	resp := request(native, "GET", "/api/capabilities", "", cookie, 200)
	var caps struct {
		Version int      `json:"api_version"`
		Clients []string `json:"clients"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil || caps.Version != 1 || len(caps.Clients) != 3 {
		t.Fatalf("invalid capabilities: %+v, %v", caps, err)
	}
	resp.Body.Close()
	base := "/api/bots/main"
	for i, client := range clients {
		credential := ""
		if i == 2 {
			credential = cookie
		}
		request(client, "POST", base+"/messages", `{"text":"client-`+strconv.Itoa(i)+`"}`, credential, 200).Body.Close()
	}
	if len(b.store.Messages()) != 3 {
		t.Fatal("clients did not write into the same conversation")
	}

	replay := func(client *http.Client, cookie, cursor string, header bool) []Event {
		t.Helper()
		path := srv.URL + base + "/stream"
		if cursor != "" && !header {
			path += "?after=" + cursor
		}
		req, _ := http.NewRequest("GET", path, nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if header {
			req.Header.Set("Last-Event-ID", cursor)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("stream: %d", resp.StatusCode)
		}
		var events []Event
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event.Kind == "replay.done" {
				return events
			}
			events = append(events, event)
		}
		t.Fatalf("stream ended before replay.done: %v", scanner.Err())
		return nil
	}
	events := replay(clients[1], "", "", false)
	if len(events) != 3 {
		t.Fatalf("WebView replay: got %d events", len(events))
	}
	cursor := strconv.FormatInt(events[len(events)-1].Seq, 10)
	request(native, "POST", base+"/messages", `{"text":"after-disconnect"}`, cookie, 200).Body.Close()
	for _, header := range []bool{false, true} {
		resumed := replay(native, cookie, cursor, header)
		if len(resumed) != 1 || !strings.Contains(string(resumed[0].Data), "after-disconnect") {
			t.Fatalf("resume header=%t: %+v", header, resumed)
		}
	}
	request(clients[1], "POST", "/api/auth/password", `{"old_password":"correct-horse","new_password":"battery-staple"}`, "", 200).Body.Close()
	request(clients[0], "GET", base+"/info", "", "", 401).Body.Close()
	request(native, "GET", base+"/info", "", cookie, 401).Body.Close()
	request(clients[1], "GET", base+"/info", "", "", 200).Body.Close()
}
