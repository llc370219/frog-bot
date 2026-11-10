package auth

import (
	"encoding/json"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"strings"
	"time"
)

// 不需要登录就能访问的路径：登录页本身和它用到的图标。
func public(path string) bool {
	switch path {
	case "/login", "/favicon.ico", "/frog.png", "/api/auth/login", "/api/auth/me", "/api/health":
		return true
	}
	return false
}

// Middleware 要求登录：没登录时 /api/* 返回 401，页面跳到 /login；写请求还要通过同源检查。
func (a *Auth) Middleware(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ok := a.Authenticated(r)
		user := ""
		if ok {
			user = a.Username()
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "authenticated": ok, "username": user})
	})
	mux.HandleFunc("POST /api/auth/password", a.changePassword)
	mux.Handle("/", next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if !SameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "拒绝跨站请求"})
			return
		}
		if !public(r.URL.Path) {
			ctx, cancel, ok := a.sessionContext(r)
			if ok {
				defer cancel()
				mux.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "请先登录", "login": "/login"})
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "请求必须使用 JSON"})
		return
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	ip, now := clientIP(r), a.now()
	if d := a.lim.blocked(ip, now); d > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(d.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": fmt.Sprintf("密码错误次数太多，请 %s后再试", roundUp(d))})
		return
	}
	if !a.check(in.Username, in.Password) {
		a.lim.fail(ip, now)
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "用户名或密码不对"})
		return
	}
	a.lim.ok(ip)
	a.setCookie(w, r, a.issue(), int(sessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": a.Username()})
}

func roundUp(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%d 分钟", int((d+time.Minute-1)/time.Minute))
	}
	return fmt.Sprintf("%d 秒", int((d+time.Second-1)/time.Second))
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	a.setCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *Auth) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
		return
	}
	ip, now := clientIP(r), a.now()
	if d := a.lim.blocked(ip, now); d > 0 {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": fmt.Sprintf("密码错误次数太多，请 %s后再试", roundUp(d))})
		return
	}
	token, err := a.ChangePassword(in.Old, in.New)
	if err != nil {
		if err.Error() == "当前密码不对" {
			a.lim.fail(ip, now)
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.setCookie(w, r, token, int(sessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *Auth) loginPage(w http.ResponseWriter, r *http.Request) {
	if a.Authenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'")
	_ = loginTmpl.Execute(w, map[string]bool{"Insecure": !secureRequest(r)})
}

// 登录页：暖象牙白、极简、无渐变，和管理页面的风格一致。
var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>登录 · frog bot</title><link rel="icon" href="/favicon.ico">
<style>
:root{--bg:#faf9f5;--card:#ffffff;--ink:#1a1915;--muted:#69675f;--line:#e6e4dd;--accent:#2f7d52;--warn:#9a6410;--err:#b4433a}
@media (prefers-color-scheme:dark){:root{--bg:#262624;--card:#2b2a27;--ink:#f5f4ef;--muted:#9e9c94;--line:#3d3c38;--accent:#62b486;--warn:#deb36d;--err:#e9918a}}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--bg);color:var(--ink);font:15px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC","Hiragino Sans GB","Microsoft YaHei","Segoe UI",sans-serif}
main{width:min(360px,calc(100vw - 32px));background:var(--card);border:1px solid var(--line);border-radius:16px;padding:32px 28px}
.brand{display:flex;align-items:center;gap:10px;margin-bottom:22px}.brand img{width:34px;height:34px}.brand b{font-size:18px;font-weight:600}
label{display:block;font-size:13px;color:var(--muted);margin:14px 0 6px}
input{width:100%;padding:10px 12px;border:1px solid var(--line);border-radius:10px;background:var(--bg);color:var(--ink);font:inherit;outline:none}
input:focus{border-color:var(--accent)}
button{width:100%;margin-top:22px;padding:10px 12px;border:0;border-radius:10px;background:var(--ink);color:var(--card);font:inherit;font-weight:600;cursor:pointer}
button:disabled{opacity:.6;cursor:default}
.err{color:var(--err);font-size:13px;min-height:20px;margin-top:12px}
.warn{color:var(--warn);font-size:12px;border:1px solid var(--line);border-radius:10px;padding:8px 10px;margin-top:18px}
.foot{color:var(--muted);font-size:12px;margin-top:14px}
</style></head><body><main>
<div class="brand"><img src="/frog.png" alt=""><b>frog bot</b></div>
<form id="f" autocomplete="on">
<label for="u">用户名</label><input id="u" name="username" autocomplete="username" required autofocus>
<label for="p">密码</label><input id="p" name="password" type="password" autocomplete="current-password" required>
<button id="b" type="submit">登录</button><div class="err" id="e" role="alert"></div>
</form>
{{if .Insecure}}<div class="warn">当前是 HTTP 明文连接，密码在网络上不加密。建议给服务器配一个域名并开启 HTTPS（见项目 README）。</div>{{end}}
<div class="foot">忘记密码：在服务器上运行 <code>sudo frogbot reset-password</code></div>
</main><script>
const f=document.getElementById('f'),b=document.getElementById('b'),e=document.getElementById('e');
f.addEventListener('submit',async(ev)=>{ev.preventDefault();b.disabled=true;e.textContent='';
try{const r=await fetch('/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'same-origin',
body:JSON.stringify({username:f.username.value.trim(),password:f.password.value})});
if(r.ok){location.replace('/');return}const d=await r.json().catch(()=>({}));e.textContent=d.error||('登录失败（'+r.status+'）')}
catch(_){e.textContent='连不上服务器'}b.disabled=false});
</script></body></html>`))
