// Package auth 是服务器模式的登录：一个管理员账号（用户名 + bcrypt 密码哈希），
// 签名的会话 Cookie（HttpOnly、SameSite=Strict，HTTPS 时加 Secure），登录失败限速，
// 以及和本机模式一致的跨站写请求拦截。本机模式不用它。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName  = "frogbot_session"
	sessionTTL  = 7 * 24 * time.Hour
	bcryptCost  = 12
	MinPassword = 8
	fileName    = "auth.json"
)

// file 是 data/auth.json 的内容（权限 0600）。只存哈希，从不存明文密码。
type file struct {
	Username  string    `json:"username"`
	Hash      string    `json:"password_hash"`
	Secret    string    `json:"session_secret"` // 给会话 Cookie 签名
	Gen       int64     `json:"generation"`     // 改密码时加一，旧会话全部失效
	UpdatedAt time.Time `json:"updated_at"`
}

type Auth struct {
	path string
	mu   sync.RWMutex
	f    file

	lim     *limiter
	now     func() time.Time
	mtime   time.Time     // auth.json 的修改时间；命令行 reset-password 改了文件后，运行中的程序自动重新读取
	revoked chan struct{} // closed whenever the authentication generation changes
	active  map[context.Context]context.CancelFunc
}

// Exists 表示数据目录里已经有管理员账号。
func Exists(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, fileName))
	return err == nil
}

// Open 读取账号文件；文件不存在时返回 os.ErrNotExist。
func Open(dataDir string) (*Auth, error) {
	a := &Auth{path: filepath.Join(dataDir, fileName), lim: newLimiter(), now: time.Now}
	raw, err := os.ReadFile(a.path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &a.f); err != nil || a.f.Username == "" || a.f.Hash == "" || len(a.f.Secret) < 32 {
		return nil, fmt.Errorf("%s 格式不对，可以用 frogbot reset-password 重新设置", a.path)
	}
	if st, err := os.Stat(a.path); err == nil {
		a.mtime = st.ModTime()
	}
	return a, nil
}

// refresh 在 auth.json 被别的进程（reset-password）改过时重新读取。
func (a *Auth) refresh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, err := os.Stat(a.path)
	if err != nil {
		return
	}
	same := st.ModTime().Equal(a.mtime)
	if same {
		return
	}
	raw, err := os.ReadFile(a.path)
	if err != nil {
		return
	}
	var f file
	if json.Unmarshal(raw, &f) != nil || f.Username == "" || f.Hash == "" || len(f.Secret) < 32 {
		return
	}
	if f.Gen != a.f.Gen || f.Secret != a.f.Secret || f.Username != a.f.Username || f.Hash != a.f.Hash {
		a.revokeLocked()
	}
	a.f, a.mtime = f, st.ModTime()
}

func (a *Auth) revokeLocked() {
	for _, cancel := range a.active {
		cancel()
	}
	if a.revoked != nil {
		close(a.revoked)
	}
	a.revoked = make(chan struct{})
}

// Bind the request lifetime to this credential generation. Snapshot the channel
// before validating the token so a simultaneous password change cannot be missed.
func (a *Auth) sessionContext(r *http.Request) (context.Context, context.CancelFunc, bool) {
	a.refresh()
	a.mu.Lock()
	if a.revoked == nil {
		a.revoked = make(chan struct{})
	}
	revoked := a.revoked
	a.mu.Unlock()
	c, err := r.Cookie(CookieName)
	if err != nil || !a.valid(c.Value) {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(r.Context())
	a.mu.Lock()
	if revoked != a.revoked {
		a.mu.Unlock()
		cancel()
		return nil, nil, false
	}
	if a.active == nil {
		a.active = map[context.Context]context.CancelFunc{}
	}
	a.active[ctx] = cancel
	a.mu.Unlock()
	go func() {
		// Also detect out-of-process reset-password and cookie expiration on idle
		// streams. Short requests stop this watcher as soon as their handler exits.
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case <-revoked:
				return
			case <-ticker.C:
				a.refresh()
				if !a.valid(c.Value) {
					return
				}
			}
		}
	}()
	return ctx, func() {
		cancel()
		a.mu.Lock()
		delete(a.active, ctx)
		a.mu.Unlock()
	}, true
}

// SetPassword 创建或覆盖管理员账号（命令行 reset-password / 安装脚本用）。所有旧会话失效。
func SetPassword(dataDir, username, password string) error {
	if err := ValidUsername(username); err != nil {
		return err
	}
	if err := ValidPassword(password); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	a := &Auth{path: filepath.Join(dataDir, fileName), lim: newLimiter(), now: time.Now}
	if raw, err := os.ReadFile(a.path); err == nil {
		_ = json.Unmarshal(raw, &a.f)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return err
	}
	a.f.Username, a.f.Hash = username, string(hash)
	a.f.Secret = randomToken(32)
	a.f.Gen++
	a.f.UpdatedAt = time.Now()
	return a.save()
}

func ValidUsername(u string) error {
	if u == "" || len(u) > 64 || strings.ContainsAny(u, " :|\t\r\n") {
		return errors.New("用户名须为 1–64 个字符，不能有空白、冒号或竖线")
	}
	return nil
}

func ValidPassword(p string) error {
	if utf8.RuneCountInString(p) < MinPassword || len(p) > 72 { // bcrypt 只看前 72 字节
		return fmt.Errorf("密码至少 %d 位，最多 72 字节", MinPassword)
	}
	return nil
}

// RandomPassword 生成一个好抄写的随机密码（去掉了 0/O、1/l/I 这类容易看错的字符）。
func RandomPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var sb strings.Builder
	for i := 0; i < 16; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[n.Int64()])
	}
	return sb.String()
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (a *Auth) save() error {
	raw, err := json.MarshalIndent(a.f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".auth-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.path)
}

func (a *Auth) Username() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.f.Username
}

// check 比对用户名和密码（用户名比较是常量时间；不存在的用户名也跑一次 bcrypt，避免时间差泄露）。
func (a *Auth) check(username, password string) bool {
	a.refresh()
	a.mu.RLock()
	f := a.f
	a.mu.RUnlock()
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(f.Username)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(f.Hash), []byte(password)) == nil
	return userOK && passOK
}

// ChangePassword 校验旧密码后设置新密码，所有旧会话失效。返回给当前浏览器的新 Cookie 值。
func (a *Auth) ChangePassword(old, next string) (string, error) {
	if !a.check(a.Username(), old) {
		return "", errors.New("当前密码不对")
	}
	if err := ValidPassword(next); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcryptCost)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	prev := a.f
	a.f.Hash, a.f.Gen, a.f.UpdatedAt = string(hash), a.f.Gen+1, a.now()
	if err := a.save(); err != nil {
		a.f = prev
		a.mu.Unlock()
		return "", err
	}
	if st, err := os.Stat(a.path); err == nil {
		a.mtime = st.ModTime()
	}
	a.revokeLocked()
	a.mu.Unlock()
	return a.issue(), nil
}

// ---------- 会话 ----------
// Cookie 值 = base64(用户名|过期时间|generation|随机数) + "." + HMAC-SHA256。重启后依然有效，改密码后全部失效。

func (a *Auth) sign(payload string) string {
	a.mu.RLock()
	secret := a.f.Secret
	a.mu.RUnlock()
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *Auth) issue() string {
	a.mu.RLock()
	user, gen := a.f.Username, a.f.Gen
	a.mu.RUnlock()
	payload := fmt.Sprintf("%s|%d|%d|%s", user, a.now().Add(sessionTTL).Unix(), gen, randomToken(9))
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return enc + "." + a.sign(enc)
}

func (a *Auth) valid(token string) bool {
	enc, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.sign(enc))) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 {
		return false
	}
	exp, err1 := strconv.ParseInt(parts[1], 10, 64)
	gen, err2 := strconv.ParseInt(parts[2], 10, 64)
	a.mu.RLock()
	defer a.mu.RUnlock()
	return err1 == nil && err2 == nil && parts[0] == a.f.Username && gen == a.f.Gen && a.now().Unix() < exp
}

// Authenticated 表示请求带着有效的会话 Cookie。
func (a *Auth) Authenticated(r *http.Request) bool {
	a.refresh()
	c, err := r.Cookie(CookieName)
	return err == nil && a.valid(c.Value)
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (a *Auth) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureRequest(r)})
}

// ---------- 登录限速 ----------
// 每个来源 IP：连续失败 5 次锁 1 分钟，之后每多错一次锁定时间翻倍（最多 1 小时）。
// 另外全局 15 分钟内失败超过 30 次，所有登录暂停 15 分钟（防止换 IP 慢慢猜）。

type limiter struct {
	mu     sync.Mutex
	ips    map[string]*failRec
	global []time.Time
	gLock  time.Time
}

type failRec struct {
	n     int
	until time.Time
	last  time.Time
}

func newLimiter() *limiter { return &limiter{ips: map[string]*failRec{}} }

func (l *limiter) blocked(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.gLock) {
		return l.gLock.Sub(now)
	}
	if f := l.ips[ip]; f != nil && now.Before(f.until) {
		return f.until.Sub(now)
	}
	return 0
}

func (l *limiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.ips[ip]
	if f == nil || now.Sub(f.last) > time.Hour {
		f = &failRec{}
		l.ips[ip] = f
	}
	f.n++
	f.last = now
	if f.n >= 5 {
		d := time.Minute << min(f.n-5, 6)
		if d > time.Hour {
			d = time.Hour
		}
		f.until = now.Add(d)
	}
	cut := now.Add(-15 * time.Minute)
	keep := l.global[:0]
	for _, t := range l.global {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.global = append(keep, now)
	if len(l.global) > 30 {
		l.gLock = now.Add(15 * time.Minute)
	}
	if len(l.ips) > 10000 { // 防止被刷爆内存
		for k, v := range l.ips {
			if now.Sub(v.last) > time.Hour {
				delete(l.ips, k)
			}
		}
	}
}

func (l *limiter) ok(ip string) {
	l.mu.Lock()
	delete(l.ips, ip)
	l.mu.Unlock()
}

// clientIP 用连接的对端地址；只有对端是本机（前面套了 Caddy/Nginx）时才信 X-Forwarded-For / X-Real-IP。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	return host
}

// ---------- 跨站拦截 ----------

// SameOrigin 判断写请求是不是来自同一个站点：有 Origin 就要求它的主机名和请求的 Host 一致；
// 主机名和端口都要相同；没有 Origin 时，浏览器标了 Sec-Fetch-Site: cross-site 也拒绝。
func SameOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
