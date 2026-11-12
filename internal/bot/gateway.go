package bot

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// cliGateway 是给外部 CLI（目前是 Pi）用的本机模型网关，思路和 opencodex 一样：CLI 只连 127.0.0.1 上的
// 这个网关，拿到的是一次运行有效的临时令牌；网关按令牌找到「哪个 API 渠道的哪个模型」，转发时才换上渠道的
// 真实 Key。Key 不进 CLI 的配置和环境变量，CLI 和它执行的命令都拿不到。
//
// 只支持 OpenAI Chat Completions（frog 的 API 渠道就是这个格式）：POST /v1/chat/completions、GET /v1/models。
type cliGateway struct {
	pool *channelPool
	base string // http://127.0.0.1:端口/v1
	srv  *http.Server

	mu     sync.Mutex
	grants map[string]gatewayGrant
}

type gatewayGrant struct {
	channel, model string
	expires        time.Time
}

// maxGatewayBody 是一次请求体的上限（带图片的对话可能比较大）。
const maxGatewayBody = 64 << 20

var gatewayUpstream = &http.Client{
	// 不设总超时：流式回复可能持续很久，由 CLI 的请求上下文控制。
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 5 * time.Minute,
		IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 8},
	CheckRedirect: noRedirect,
}

func startCLIGateway(pool *channelPool) (*cliGateway, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("启动本机模型网关失败：%w", err)
	}
	g := &cliGateway{pool: pool, base: "http://" + ln.Addr().String() + "/v1", grants: map[string]gatewayGrant{}}
	g.srv = &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() { _ = g.srv.Serve(ln) }()
	return g, nil
}

func (g *cliGateway) Close() {
	if g != nil && g.srv != nil {
		_ = g.srv.Close()
	}
}

// Issue 发一个只能用指定渠道、指定模型的临时令牌，有效期 ttl；revoke 立即作废。
func (g *cliGateway) Issue(channelID, model string, ttl time.Duration) (baseURL, token string, revoke func(), err error) {
	c, ok := g.pool.find(channelID)
	if !ok {
		return "", "", nil, errors.New("所选 API 渠道不存在了，请在「设置 → 模型与 Agent」里重新选择")
	}
	if !c.Enabled {
		return "", "", nil, fmt.Errorf("API 渠道「%s」已停用", c.Name)
	}
	if !validRoute([]APIChannel{c.APIChannel}, channelID, model) {
		return "", "", nil, fmt.Errorf("API 渠道「%s」里没有模型 %s", c.Name, model)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", "", nil, err
	}
	token = "frog-gw-" + hex.EncodeToString(raw)
	g.mu.Lock()
	now := time.Now()
	for t, gr := range g.grants { // 顺手清理过期的
		if now.After(gr.expires) {
			delete(g.grants, t)
		}
	}
	g.grants[token] = gatewayGrant{channel: channelID, model: model, expires: now.Add(ttl)}
	g.mu.Unlock()
	return g.base, token, func() {
		g.mu.Lock()
		delete(g.grants, token)
		g.mu.Unlock()
	}, nil
}

func (g *cliGateway) grant(r *http.Request) (gatewayGrant, bool) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if tok == "" {
		tok = r.Header.Get("X-Api-Key")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for t, gr := range g.grants {
		if subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			if time.Now().After(gr.expires) {
				delete(g.grants, t)
				return gatewayGrant{}, false
			}
			return gr, true
		}
	}
	return gatewayGrant{}, false
}

func gatewayError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": msg, "type": "frog_gateway_error"}})
}

func (g *cliGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	gr, ok := g.grant(r)
	if !ok {
		gatewayError(w, http.StatusUnauthorized, "frog 网关令牌无效或已过期")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list",
			"data": []map[string]any{{"id": gr.model, "object": "model", "owned_by": "frog"}}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		g.forwardChat(w, r, gr)
	default:
		gatewayError(w, http.StatusNotFound, "frog 网关只支持 /v1/chat/completions 和 /v1/models")
	}
}

func (g *cliGateway) forwardChat(w http.ResponseWriter, r *http.Request, gr gatewayGrant) {
	c, ok := g.pool.find(gr.channel)
	if !ok || !c.Enabled {
		gatewayError(w, http.StatusServiceUnavailable, "所选 API 渠道不可用了")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGatewayBody))
	if err != nil {
		gatewayError(w, http.StatusRequestEntityTooLarge, "请求太大")
		return
	}
	// 模型固定为令牌允许的那个：CLI 改了模型名也只能用设置里选的。
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		gatewayError(w, http.StatusBadRequest, "请求不是有效的 JSON")
		return
	}
	req["model"], _ = json.Marshal(gr.model)
	body, _ = json.Marshal(req)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		gatewayError(w, http.StatusBadGateway, "渠道地址无效")
		return
	}
	up.Header.Set("Content-Type", "application/json")
	if a := r.Header.Get("Accept"); a != "" {
		up.Header.Set("Accept", a)
	}
	if c.key != "" {
		up.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := gatewayUpstream.Do(up)
	if err != nil {
		gatewayError(w, http.StatusBadGateway, "连不上 API 渠道「"+c.Name+"」："+redactKey(err.Error(), c.key))
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control", "X-Request-Id", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func redactKey(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "***")
}
