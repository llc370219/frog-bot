package auth

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newServer(t *testing.T) (*Auth, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	if err := SetPassword(dir, "admin", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := http.NewServeMux()
	app.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app")) })
	app.HandleFunc("POST /api/do", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("done")) })
	app.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: hi\n\n"))
	})
	srv := httptest.NewServer(a.Middleware(app))
	t.Cleanup(srv.Close)
	return a, srv
}

func do(t *testing.T, method, url, body, cookie, origin string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", CookieName+"="+cookie)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func cookieOf(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			return c.Value
		}
	}
	return ""
}

func TestLoginFlow(t *testing.T) {
	a, srv := newServer(t)
	if r := do(t, "GET", srv.URL+"/api/stream", "", "", ""); r.StatusCode != 401 {
		t.Fatalf("unauthenticated API: %d", r.StatusCode)
	}
	if r := do(t, "GET", srv.URL+"/", "", "", ""); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/login" {
		t.Fatalf("unauthenticated page should redirect: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := do(t, "GET", srv.URL+"/login", "", "", ""); r.StatusCode != 200 {
		t.Fatalf("login page: %d", r.StatusCode)
	}
	if r := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"admin","password":"wrong-pass"}`, "", ""); r.StatusCode != 401 {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	r := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"admin","password":"correct-horse"}`, "", srv.URL)
	tok := cookieOf(r)
	if r.StatusCode != 200 || tok == "" {
		t.Fatalf("login: %d", r.StatusCode)
	}
	for _, c := range r.Cookies() {
		if c.Name == CookieName && (!c.HttpOnly || c.SameSite != http.SameSiteStrictMode) {
			t.Fatal("cookie must be HttpOnly + SameSite=Strict")
		}
	}
	if r := do(t, "GET", srv.URL+"/", "", tok, ""); r.StatusCode != 200 {
		t.Fatalf("page with cookie: %d", r.StatusCode)
	}
	// SSE 带 Cookie 可以连
	r = do(t, "GET", srv.URL+"/api/stream", "", tok, "")
	line, _ := bufio.NewReader(r.Body).ReadString('\n')
	if r.StatusCode != 200 || !strings.HasPrefix(line, "data: hi") {
		t.Fatalf("SSE with cookie: %d %q", r.StatusCode, line)
	}
	// 同源写请求通过；跨站写请求（即使带着 Cookie）被拒
	if r := do(t, "POST", srv.URL+"/api/do", "{}", tok, srv.URL); r.StatusCode != 200 {
		t.Fatalf("same-origin POST: %d", r.StatusCode)
	}
	if r := do(t, "POST", srv.URL+"/api/do", "{}", tok, "https://evil.example"); r.StatusCode != 403 {
		t.Fatalf("cross-site POST: %d", r.StatusCode)
	}
	// 伪造 / 篡改的 Cookie 无效
	if r := do(t, "GET", srv.URL+"/api/stream", "", tok+"x", ""); r.StatusCode != 401 {
		t.Fatalf("tampered cookie: %d", r.StatusCode)
	}
	// 改密码：旧会话失效，新 Cookie 有效
	r = do(t, "POST", srv.URL+"/api/auth/password", `{"old_password":"correct-horse","new_password":"battery-staple"}`, tok, srv.URL)
	tok2 := cookieOf(r)
	if r.StatusCode != 200 || tok2 == "" {
		t.Fatalf("change password: %d", r.StatusCode)
	}
	if r := do(t, "GET", srv.URL+"/api/stream", "", tok, ""); r.StatusCode != 401 {
		t.Fatal("old session should be invalid after password change")
	}
	if r := do(t, "GET", srv.URL+"/api/stream", "", tok2, ""); r.StatusCode != 200 {
		t.Fatal("new session should work")
	}
	// 命令行 reset-password 改了文件，运行中的程序自动生效
	time.Sleep(10 * time.Millisecond)
	if err := SetPassword(strings.TrimSuffix(a.path, "/"+fileName), "admin", "reset-by-cli"); err != nil {
		t.Fatal(err)
	}
	if r := do(t, "GET", srv.URL+"/api/stream", "", tok2, ""); r.StatusCode != 401 {
		t.Fatal("session should be invalid after CLI reset")
	}
	if r := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"admin","password":"reset-by-cli"}`, "", ""); r.StatusCode != 200 {
		t.Fatalf("login with reset password: %d", r.StatusCode)
	}
}

func TestPasswordChangeCancelsActiveRequest(t *testing.T) {
	dir := t.TempDir()
	if err := SetPassword(dir, "admin", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	app := http.NewServeMux()
	app.HandleFunc("GET /api/long", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
	})
	srv := httptest.NewServer(a.Middleware(app))
	defer srv.Close()

	login := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"admin","password":"correct-horse"}`, "", "")
	token := cookieOf(login)
	if token == "" {
		t.Fatal("login did not issue a session")
	}
	req, err := http.NewRequest("GET", srv.URL+"/api/long", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", CookieName+"="+token)
	requestDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("authenticated handler did not start")
	}
	if _, err := a.ChangePassword("correct-horse", "new-correct-horse"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("password change did not cancel the active request")
	}
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not finish after its session was revoked")
	}
}

func TestLoginRateLimit(t *testing.T) {
	_, srv := newServer(t)
	for i := 0; i < 5; i++ {
		if r := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"admin","password":"nope-nope"}`, "", ""); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	// 第 6 次：即使密码正确也先被限速
	r := do(t, "POST", srv.URL+"/api/auth/login", `{"username":"admin","password":"correct-horse"}`, "", "")
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 429, got %d", r.StatusCode)
	}
}

func TestLimiterBackoffAndGlobalLock(t *testing.T) {
	l := newLimiter()
	now := time.Now()
	for i := 0; i < 5; i++ {
		l.fail("1.1.1.1", now)
	}
	if d := l.blocked("1.1.1.1", now); d < 59*time.Second || d > time.Minute {
		t.Fatalf("first lock %v", d)
	}
	if l.blocked("2.2.2.2", now) != 0 {
		t.Fatal("other IP should not be blocked yet")
	}
	l.fail("1.1.1.1", now)
	if d := l.blocked("1.1.1.1", now); d < 2*time.Minute-time.Second {
		t.Fatalf("backoff should double: %v", d)
	}
	for i := 0; i < 30; i++ {
		l.fail("3.3.3."+string(rune('0'+i%10)), now)
	}
	if l.blocked("9.9.9.9", now) == 0 {
		t.Fatal("global lock should kick in after many failures")
	}
	_ = context.Background()
}

func TestRandomPasswordValid(t *testing.T) {
	p := RandomPassword()
	if ValidPassword(p) != nil || len(p) != 19 {
		t.Fatalf("bad random password %q", p)
	}
	if ValidUsername("ad min") == nil || ValidUsername("a:b") == nil || ValidUsername("admin") != nil {
		t.Fatal("username validation")
	}
}
