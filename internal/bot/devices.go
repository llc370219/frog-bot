package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The peer listener is separate from the administration UI, even in server mode.
const peerPrefix = "/peer/v1"
const deviceHeader = "X-Frogbot-Device"
const maxPeerBody = 64 << 10

type DeviceConfig struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Listen  string       `json:"listen"`
	TLSCert string       `json:"tls_cert,omitempty"`
	TLSKey  string       `json:"tls_key,omitempty"`
	Peers   []PeerConfig `json:"peers"`
}

type PeerConfig struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

var deviceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
var peerTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{32,128}$`)

func LoadDeviceConfig(path string) (*DeviceConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 128<<10))
	dec.DisallowUnknownFields()
	var cfg DeviceConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("设备配置只能包含一个 JSON 对象")
	}
	return &cfg, cfg.validate()
}

func (c *DeviceConfig) validate() error {
	if !deviceIDPattern.MatchString(c.ID) || c.ID == "local" || !validDeviceName(c.Name) {
		return errors.New("设备配置需要唯一的 id（小写字母、数字或连字符）和名称")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || port == "" {
		return errors.New("设备配置需要 listen 地址，例如 127.0.0.1:8081")
	}
	tls := c.TLSCert != "" && c.TLSKey != ""
	if (c.TLSCert == "") != (c.TLSKey == "") || (!tls && !isPeerLoopback(host)) {
		return errors.New("设备端口对外监听时需要 tls_cert 和 tls_key；反向代理后可监听 127.0.0.1")
	}
	if len(c.Peers) > 16 {
		return errors.New("最多配置 16 台对端设备")
	}
	ids, tokens := map[string]bool{c.ID: true, "local": true}, map[string]bool{}
	for _, p := range c.Peers {
		if !deviceIDPattern.MatchString(p.ID) || ids[p.ID] || !validDeviceName(p.Name) {
			return errors.New("对端设备的 id 或名称无效，或 id 重复")
		}
		u, err := url.Parse(p.URL)
		if err != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
			(u.Path != "" && u.Path != "/") || u.RawPath != "" ||
			(u.Scheme != "https" && !(u.Scheme == "http" && isPeerLoopback(u.Hostname()))) {
			return fmt.Errorf("设备 %s 的 url 必须是 HTTPS 根地址，HTTP 仅支持本机回环", p.ID)
		}
		if !peerTokenPattern.MatchString(p.Token) || tokens[p.Token] {
			return fmt.Errorf("设备 %s 需要独立的设备令牌（32–128 位字母、数字、下划线或连字符）", p.ID)
		}
		ids[p.ID], tokens[p.Token] = true, true
	}
	return nil
}

func validDeviceName(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 60 && !strings.ContainsAny(s, "\r\n")
}

func isPeerLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type peerNetwork struct {
	id, name       string
	peers          []PeerConfig
	client         *http.Client
	settingsClient *http.Client
	mu             sync.Mutex
	cache          map[string]deviceGroup
}

type deviceGroup struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Online bool         `json:"online"`
	Bots   []BotProfile `json:"bots"`
}

func newPeerNetwork(cfg *DeviceConfig) (*peerNetwork, error) {
	if cfg == nil {
		return nil, nil
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 6 * time.Second
	settingsTransport := transport.Clone()
	settingsTransport.ResponseHeaderTimeout = 30 * time.Second // model test can take up to 25 seconds
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &peerNetwork{id: cfg.ID, name: cfg.Name, peers: slices.Clone(cfg.Peers), cache: map[string]deviceGroup{},
		client:         &http.Client{Transport: transport, CheckRedirect: noRedirect},
		settingsClient: &http.Client{Transport: settingsTransport, CheckRedirect: noRedirect, Timeout: 35 * time.Second}}, nil
}

func (n *peerNetwork) find(id string) (PeerConfig, bool) {
	if n != nil {
		for _, p := range n.peers {
			if p.ID == id {
				return p, true
			}
		}
	}
	return PeerConfig{}, false
}

func (n *peerNetwork) request(ctx context.Context, p PeerConfig, method, path string, body io.Reader, last string) (*http.Response, error) {
	headers := make(http.Header)
	if body != nil {
		headers.Set("Content-Type", "application/json")
	}
	if last != "" {
		headers.Set("Last-Event-ID", last)
	}
	resp, err := n.do(ctx, p, method, path, body, headers)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("设备「%s」拒绝了请求（HTTP %d），请检查设备配置及 Bot 是否存在", p.Name, resp.StatusCode)
	}
	return resp, nil
}

func (n *peerNetwork) do(ctx context.Context, p PeerConfig, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	return n.doRequest(ctx, n.client, p, method, path, body, headers)
}

func (n *peerNetwork) doRequest(ctx context.Context, client *http.Client, p PeerConfig, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.URL, "/")+peerPrefix+path, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	req.Header.Set(deviceHeader, n.id)
	req.Header.Set("Authorization", "Bearer "+p.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接设备「%s」", p.Name)
	}
	if resp.Header.Get(deviceHeader) != p.ID {
		resp.Body.Close()
		return nil, fmt.Errorf("设备「%s」拒绝了请求（HTTP %d），请检查设备配置及 Bot 是否存在", p.Name, resp.StatusCode)
	}
	return resp, nil
}

func (bs *Bots) localProfiles() []BotProfile {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return slices.Clone(bs.profiles)
}

func (bs *Bots) publicProfiles() []BotProfile {
	profiles := bs.localProfiles()
	for i := range profiles {
		profiles[i].Instructions = ""
	}
	return profiles
}

func (bs *Bots) deviceGroups(ctx context.Context) []deviceGroup {
	name, _ := os.Hostname()
	if !validDeviceName(name) {
		name = "本机"
	}
	if bs.network != nil {
		name = bs.network.name
	}
	groups := []deviceGroup{{ID: "local", Name: name, Online: true, Bots: bs.localProfiles()}}
	n := bs.network
	if n == nil {
		return groups
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	groups = append(groups, make([]deviceGroup, len(n.peers))...)
	var wg sync.WaitGroup
	for i, p := range n.peers {
		wg.Add(1)
		go func(i int, p PeerConfig) {
			defer wg.Done()
			g := deviceGroup{ID: p.ID, Name: p.Name, Bots: []BotProfile{}}
			resp, err := n.request(ctx, p, "GET", "/bots", nil, "")
			if err == nil {
				defer resp.Body.Close()
				var profiles []BotProfile
				if err := json.NewDecoder(io.LimitReader(resp.Body, maxPeerBody)).Decode(&profiles); err == nil && validateProfiles(profiles) == nil {
					g.Online = true
					for _, b := range profiles {
						if validDeviceName(b.Name) {
							b.Instructions = ""
							g.Bots = append(g.Bots, b)
						}
					}
				}
			}
			n.mu.Lock()
			if g.Online {
				n.cache[p.ID] = g
			} else if prior, ok := n.cache[p.ID]; ok {
				g.Bots = prior.Bots
			}
			n.mu.Unlock()
			groups[i+1] = g
		}(i, p)
	}
	wg.Wait()
	return groups
}

func validBotID(id string) bool { return id == defaultBotID || botIDPattern.MatchString(id) }

func (bs *Bots) acquireBot(id string) (*Bot, func()) {
	b, _, release := bs.acquireBotEndpoint(id)
	return b, release
}

func (bs *Bots) acquireBotEndpoint(id string) (*Bot, *botEndpoint, func()) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	endpoint := bs.handlers[id]
	if bs.closed || endpoint == nil {
		return nil, nil, func() {}
	}
	endpoint.requests.Add(1)
	return bs.runtimes[id], endpoint, endpoint.requests.Done
}
