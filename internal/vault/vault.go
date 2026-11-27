// Package vault 是 frogbot 的密钥库：AES-256-GCM 加密存储，模型永远看不到明文。
//
// 文件：
//   - data/secrets.json  加密后的记录（名字、密文、描述、使用范围、时间）
//   - data/master.key    自动生成的主密钥（0600），或用环境变量 FROGBOT_MASTER_KEY 提供
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"frogbot/internal/atomicfile"
)

var nameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// 这些名字会和 shell 的常用环境变量冲突，不允许用作密钥名。
var reserved = map[string]bool{"PATH": true, "HOME": true, "USER": true, "SHELL": true, "PWD": true,
	"LANG": true, "TERM": true, "TMPDIR": true, "IFS": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true}

// ValidName 检查密钥名（UPPER_SNAKE）。
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("密钥名要用大写字母、数字和下划线（例如 GITHUB_TOKEN）")
	}
	if reserved[name] {
		return fmt.Errorf("%s 是系统环境变量名，换一个名字", name)
	}
	return nil
}

// Scope 限制密钥能被谁用；都为空表示不限制（仍然需要审批）。
type Scope struct {
	Tools   []string `json:"tools,omitempty"`   // 允许的工具名，如 shell
	Domains []string `json:"domains,omitempty"` // 允许出现在参数里的域名，如 api.github.com
}

// Info 是可以给模型和前端看的信息（没有值）。
type Info struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	PurposeRule string     `json:"purpose_rule,omitempty"` // 自然语言用途规则（如"仅限用于 imap.qq.com 收信或 *.example.com 签到"）
	Scope       Scope      `json:"scope"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastUsed    *time.Time `json:"last_used,omitempty"`
}

type record struct {
	Info
	Cipher string `json:"cipher"` // base64(nonce || ciphertext)
}

type fileFormat struct {
	Version int       `json:"version"`
	Secrets []*record `json:"secrets"`
}

// Vault 是线程安全的加密密钥库。
type Vault struct {
	path string
	aead cipher.AEAD
	Now  func() time.Time

	mu        sync.Mutex
	recs      map[string]*record
	plain     map[string]string // 解密后的明文只在内存里，用于替换和脱敏
	repl      *strings.Replacer
	scrubRepl *strings.Replacer // V2 多编码+高熵首尾片段脱敏 -> [REDACTED:secret://xxx]
}

// LoadKey 读取主密钥：优先环境变量（64 位 hex / base64 的 32 字节；其他字符串会做 SHA-256），
// 否则读 dir/master.key，没有就生成一个（权限 0600）。generated 表示刚刚新建。
func LoadKey(envVal, dir string) (key []byte, generated bool, err error) {
	if envVal = strings.TrimSpace(envVal); envVal != "" {
		return parseKey(envVal), false, nil
	}
	p := filepath.Join(dir, "master.key")
	if b, err := os.ReadFile(p); err == nil {
		return parseKey(strings.TrimSpace(string(b))), false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return key, true, nil
}

func parseKey(s string) []byte {
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b
		}
	}
	h := sha256.Sum256([]byte(s)) // 口令形式：可用，但不如随机 32 字节
	return h[:]
}

// Open 打开（或新建）dir/secrets.json。
func Open(dir string, key []byte) (*Vault, error) {
	return OpenFile(filepath.Join(dir, "secrets.json"), key)
}

// OpenFile 打开（或新建）指定路径的密钥箱（如 /data/host/vault.enc）。
func OpenFile(path string, key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("主密钥必须是 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	v := &Vault{path: path, aead: aead, recs: map[string]*record{}, plain: map[string]string{}}
	b, err := os.ReadFile(v.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		var f fileFormat
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s 损坏: %w", filepath.Base(path), err)
		}
		for _, r := range f.Secrets {
			pt, err := v.decrypt(r.Name, r.Cipher)
			if err != nil {
				return nil, fmt.Errorf("无法解密密钥 %s（主密钥不对？）", r.Name)
			}
			v.recs[r.Name] = r
			v.plain[r.Name] = pt
		}
	}
	v.rebuild()
	return v, nil
}

func (v *Vault) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// The API credential is not part of the tool-accessible secret catalog.
func (v *Vault) SealAPIKey(key string) (string, error) {
	return v.encrypt("settings:api-key", key)
}

func (v *Vault) OpenAPIKey(sealed string) (string, error) {
	return v.decrypt("settings:api-key", sealed)
}

func (v *Vault) encrypt(name, plain string) (string, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := v.aead.Seal(nonce, nonce, []byte(plain), []byte("frogbot:"+name)) // 名字作为附加数据，密文不能挪给别的名字用
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (v *Vault) decrypt(name, c string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(c)
	if err != nil || len(b) < v.aead.NonceSize() {
		return "", errors.New("密文格式不对")
	}
	ns := v.aead.NonceSize()
	pt, err := v.aead.Open(nil, b[:ns], b[ns:], []byte("frogbot:"+name))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// save 写盘（先写临时文件再改名），调用方持锁。
func (v *Vault) save() error {
	f := fileFormat{Version: 1}
	for _, r := range v.recs {
		f.Secrets = append(f.Secrets, r)
	}
	sort.Slice(f.Secrets, func(i, j int) bool { return f.Secrets[i].Name < f.Secrets[j].Name })
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(v.path, b, 0o600)
}

// Set 新建或更新一个密钥。value 不会出现在任何错误信息里。
func (v *Vault) Set(name, value, desc string, scope *Scope) error {
	return v.SetWithRule(name, value, desc, "", scope)
}

var handleRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{1,63}$`)

// NormalizeHandle 将 secret://foo-bar 或 FOO_BAR 规范为 vault 键名（兼容历史 UPPER_SNAKE 与 V2 小写句柄）。
func NormalizeHandle(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "secret://")
	return name
}

// SetWithRule 新建或更新密钥并绑定自然语言用途规则（V2 Blind Vault）。
func (v *Vault) SetWithRule(name, value, desc, purposeRule string, scope *Scope) error {
	name = NormalizeHandle(name)
	if purposeRule == "" {
		if err := ValidName(name); err != nil {
			return err
		}
	} else {
		if !handleRe.MatchString(name) || reserved[strings.ToUpper(name)] {
			return fmt.Errorf("无效的密钥句柄名: %s", name)
		}
	}
	if value == "" {
		return errors.New("密钥值不能为空")
	}
	if len(value) > 16<<10 {
		return errors.New("密钥值太长（最多 16KB）")
	}
	c, err := v.encrypt(name, value)
	if err != nil {
		return errors.New("加密失败")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	previous := v.recs[name]
	var r *record
	if previous == nil {
		r = &record{Info: Info{Name: name, CreatedAt: now}}
	} else {
		cp := *previous
		r = &cp
	}
	r.Cipher, r.UpdatedAt = c, now
	if desc != "" {
		r.Description = desc
	}
	if purposeRule != "" {
		r.PurposeRule = purposeRule
	}
	if scope != nil {
		r.Scope = Scope{Tools: append([]string(nil), scope.Tools...), Domains: append([]string(nil), scope.Domains...)}
	}
	v.recs[name] = r
	old, had := v.plain[name]
	v.plain[name] = value
	if err := v.save(); err != nil {
		if had {
			v.plain[name] = old
			v.recs[name] = previous
		} else {
			delete(v.plain, name)
			delete(v.recs, name)
		}
		return fmt.Errorf("保存失败: %v", err)
	}
	v.rebuild()
	return nil
}

// GetInfo 返回单个密钥的元数据（不含明文）。
func (v *Vault) GetInfo(name string) (Info, bool) {
	name = NormalizeHandle(name)
	v.mu.Lock()
	defer v.mu.Unlock()
	r := v.recs[name]
	if r == nil {
		return Info{}, false
	}
	cp := r.Info
	cp.Scope.Tools = append([]string(nil), r.Scope.Tools...)
	cp.Scope.Domains = append([]string(nil), r.Scope.Domains...)
	return cp, true
}

// AddWhitelistDomain 将域名永久加入指定密钥的白名单并落盘。
func (v *Vault) AddWhitelistDomain(name, domain string) error {
	name = NormalizeHandle(name)
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return errors.New("白名单域名不能为空")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	r := v.recs[name]
	if r == nil {
		return fmt.Errorf("密钥 %s 不存在", name)
	}
	for _, existing := range r.Scope.Domains {
		if strings.EqualFold(existing, domain) {
			return nil
		}
	}
	prev := append([]string(nil), r.Scope.Domains...)
	r.Scope.Domains = append(r.Scope.Domains, domain)
	r.UpdatedAt = v.now()
	if err := v.save(); err != nil {
		r.Scope.Domains = prev
		return err
	}
	return nil
}

// DomainAllowed 判断某个域名是否已被指定密钥的白名单覆盖。
func (v *Vault) DomainAllowed(name, host string) bool {
	name = NormalizeHandle(name)
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	r := v.recs[name]
	if r == nil || len(r.Scope.Domains) == 0 {
		return false
	}
	return domainOK(host, r.Scope.Domains)
}

// Delete 删除一个密钥。
func (v *Vault) Delete(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.recs[name] == nil {
		return fmt.Errorf("没有叫 %s 的密钥", name)
	}
	previous, plain := v.recs[name], v.plain[name]
	delete(v.recs, name)
	delete(v.plain, name) // 删掉后旧值不再脱敏：它已经不会被注入任何工具
	if err := v.save(); err != nil {
		v.recs[name], v.plain[name] = previous, plain
		return err
	}
	v.rebuild()
	return nil
}

// List 返回所有密钥的信息（不含值）。
func (v *Vault) List() []Info {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]Info, 0, len(v.recs))
	for _, r := range v.recs {
		out = append(out, r.Info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Has 判断密钥是否存在。
func (v *Vault) Has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.recs[name] != nil
}

var urlHost = regexp.MustCompile(`(?i)\b(?:https?|wss?|ftp)://([a-z0-9.\-]+)`)

// Allowed 实现 onefrog.Secrets：密钥存在 + 工具/域名在允许范围内。
func (v *Vault) Allowed(tool string, args json.RawMessage, names []string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, n := range names {
		r := v.recs[n]
		if r == nil {
			return fmt.Errorf("密钥 %s 不存在，先用 request_secret 让用户保存", n)
		}
		if len(r.Scope.Tools) > 0 && !contains(r.Scope.Tools, tool) {
			return fmt.Errorf("密钥 %s 只允许这些工具使用: %s", n, strings.Join(r.Scope.Tools, ", "))
		}
		if len(r.Scope.Domains) > 0 {
			for _, m := range urlHost.FindAllStringSubmatch(string(args), -1) {
				if !domainOK(strings.ToLower(m[1]), r.Scope.Domains) {
					return fmt.Errorf("密钥 %s 不允许发往 %s（允许: %s）", n, m[1], strings.Join(r.Scope.Domains, ", "))
				}
			}
		}
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func domainOK(host string, allowed []string) bool {
	for _, d := range allowed {
		d = strings.ToLower(strings.TrimPrefix(d, "*."))
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Values 实现 onefrog.Secrets：返回明文并更新 last_used。只在工具执行层调用。
func (v *Vault) Values(names []string) (map[string]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]string{}
	now := v.now()
	for _, n := range names {
		p, ok := v.plain[n]
		if !ok {
			return nil, fmt.Errorf("密钥 %s 不存在", n)
		}
		out[n] = p
	}
	previous := map[string]*time.Time{}
	for _, n := range names {
		if _, seen := previous[n]; !seen {
			previous[n] = v.recs[n].LastUsed
		}
		t := now
		v.recs[n].LastUsed = &t
	}
	if err := v.save(); err != nil {
		for n, used := range previous {
			v.recs[n].LastUsed = used
		}
		return nil, fmt.Errorf("保存密钥使用记录失败: %w", err)
	}
	return out, nil
}

// Redact 实现 onefrog.Secrets：把所有已知密钥值及其常见编码替换成 [secret:NAME]。
func (v *Vault) Redact(s string) string {
	v.mu.Lock()
	r := v.repl
	v.mu.Unlock()
	if r == nil || s == "" {
		return s
	}
	return r.Replace(s)
}

// RedactBytes 同上，作用于字节（例如写盘前的 JSON）。
func (v *Vault) RedactBytes(b []byte) []byte {
	v.mu.Lock()
	r := v.repl
	v.mu.Unlock()
	if r == nil {
		return b
	}
	return []byte(r.Replace(string(b)))
}

// Scrub 是 V2 增强版输出脱敏：把密钥原文、多编码（URL/Base64/Hex/JSON）及高熵首尾片段替换为 [REDACTED:secret://name]。
func (v *Vault) Scrub(s string) string {
	v.mu.Lock()
	r := v.scrubRepl
	v.mu.Unlock()
	if r == nil || s == "" {
		return s
	}
	return r.Replace(s)
}

// ScrubBytes 同 Scrub，作用于字节切片。
func (v *Vault) ScrubBytes(b []byte) []byte {
	v.mu.Lock()
	r := v.scrubRepl
	v.mu.Unlock()
	if r == nil {
		return b
	}
	return []byte(r.Replace(string(b)))
}

// rebuild 重新生成脱敏替换表，调用方持锁。
func (v *Vault) rebuild() {
	type pair struct{ from, to string }
	var pairs []pair
	var scrubPairs []pair
	seen := map[string]bool{}
	scrubSeen := map[string]bool{}
	for name, val := range v.plain {
		tag := "[secret:" + name + "]"
		v2Tag := "[REDACTED:secret://" + name + "]"
		for _, f := range Variants(val) {
			if !seen[f] {
				seen[f] = true
				pairs = append(pairs, pair{f, tag})
			}
			if !scrubSeen[f] {
				scrubSeen[f] = true
				scrubPairs = append(scrubPairs, pair{f, v2Tag})
			}
		}
		for _, seg := range HighEntropyEdgeSegments(val) {
			if !scrubSeen[seg] {
				scrubSeen[seg] = true
				scrubPairs = append(scrubPairs, pair{seg, v2Tag})
			}
		}
	}
	if len(pairs) == 0 {
		v.repl = nil
		v.scrubRepl = nil
		return
	}
	sortPairs := func(ps []pair) *strings.Replacer {
		sort.Slice(ps, func(i, j int) bool {
			if len(ps[i].from) != len(ps[j].from) {
				return len(ps[i].from) > len(ps[j].from)
			}
			return ps[i].from < ps[j].from
		})
		args := make([]string, 0, len(ps)*2)
		for _, p := range ps {
			args = append(args, p.from, p.to)
		}
		return strings.NewReplacer(args...)
	}
	v.repl = sortPairs(pairs)
	v.scrubRepl = sortPairs(scrubPairs)
}

// HighEntropyEdgeSegments 提取长密钥的高熵首尾片段（>=8 字符、多字符类），防止只泄露前缀或后缀。
func HighEntropyEdgeSegments(val string) []string {
	if len(val) < 16 {
		return nil
	}
	var out []string
	segLen := 8
	if len(val) >= 24 {
		segLen = 10
	}
	for _, cand := range []string{val[:segLen], val[len(val)-segLen:]} {
		if looksHighEntropy(cand) {
			out = append(out, cand)
		}
	}
	return out
}

func looksHighEntropy(s string) bool {
	if len(s) < 8 {
		return false
	}
	seen := map[byte]bool{}
	var hasLower, hasUpper, hasDigit, hasSym bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		seen[c] = true
		switch {
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= '0' && c <= '9':
			hasDigit = true
		default:
			hasSym = true
		}
	}
	classes := 0
	for _, b := range []bool{hasLower, hasUpper, hasDigit, hasSym} {
		if b {
			classes++
		}
	}
	return len(seen) >= 6 && classes >= 2
}

// Variants 返回一个密钥值需要被抹掉的各种形式：原文、JSON 转义、URL 编码、hex、
// base64（标准/URL 安全，带不带 = 都算），以及它出现在更长 base64 串中间时
// 一定会出现的那段字符（例如 Basic auth 里 base64("user:"+token)）。
func Variants(val string) []string {
	if len(val) < 4 {
		return nil
	}
	set := map[string]bool{val: true}
	add := func(s string, minLen int) {
		if len(s) >= minLen {
			set[s] = true
		}
	}
	if j, err := json.Marshal(val); err == nil {
		add(string(j[1:len(j)-1]), 4)
	}
	if len(val) >= 6 {
		add(url.QueryEscape(val), 6)
		add(url.PathEscape(val), 6)
		add(hex.EncodeToString([]byte(val)), 12)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
			add(enc.EncodeToString([]byte(val)), 8)
		}
		for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			for k := 0; k < 3; k++ {
				buf := append(make([]byte, k), val...)
				e := enc.EncodeToString(buf)
				start := (8*k + 5) / 6          // 第一个完全由密钥字节决定的字符
				end := (8 * (k + len(val))) / 6 // 最后一个完全由密钥字节决定的字符之后
				if end > len(e) {
					end = len(e)
				}
				if end-start >= 8 {
					add(e[start:end], 8)
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}
