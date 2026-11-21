package remote

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newMockFrogApp() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"id": "local", "name": "蛙蛙服务器", "online": true, "bots": []any{}},
		})
	})
	mux.HandleFunc("GET /api/bots/main/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 1; i <= 3; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			_, _ = fmt.Fprintf(w, "data: {\"seq\":%d,\"kind\":\"ping\"}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(15 * time.Millisecond)
		}
	})
	return mux
}

func httpClientViaTunnel(tun *ClientTunnel) *http.Client {
	pu, _ := url.Parse(tun.ProxyURL())
	return &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(pu),
		},
	}
}

func TestRealityTunnelEndToEndAndCamouflage(t *testing.T) {
	// 1. 启动本地模拟伪装站点（代替 www.apple.com:443，离线可测）
	camouflage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Camouflage", "apple-mock")
		_, _ = w.Write([]byte("welcome to camouflage site"))
	}))
	defer camouflage.Close()
	camoAddr := camouflage.Listener.Addr().String()

	// 2. 启动本机内网模拟服务（验证通过内置 sing-box 隧道可直通主机内网服务）
	lanSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("internal-lan-ok"))
	}))
	defer lanSvc.Close()

	mgr, err := NewManager(t.TempDir(), newMockFrogApp())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	mgr.SetCamouflageDest(camoAddr)
	st, err := mgr.UpdateConfig(SaveInput{
		Mode:          ModeReality,
		RealityListen: "127.0.0.1:0",
		PublicHost:    "127.0.0.1",
		SNI:           "example.com",
		ServerName:    "测试云主机",
	})
	if err != nil {
		t.Fatalf("UpdateConfig reality: %v", err)
	}
	if !st.Ready {
		t.Fatalf("expected Ready=true, got %+v", st)
	}

	// 3. 验证未授权扫描器探测 REALITY 端口时，完全看不到 frog，而是拿到伪装站点证书与响应
	parts := strings.Split(st.StatusText, "· ")
	realityHostPort := strings.TrimSpace(parts[len(parts)-1])
	probeClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				ServerName:         "example.com",
				InsecureSkipVerify: true,
			},
		},
	}
	probeResp, err := probeClient.Get("https://" + realityHostPort + "/")
	if err != nil {
		t.Fatalf("camouflage probe failed: %v", err)
	}
	probeBody, _ := io.ReadAll(probeResp.Body)
	probeResp.Body.Close()
	if probeResp.Header.Get("X-Camouflage") != "apple-mock" || string(probeBody) != "welcome to camouflage site" {
		t.Fatalf("scanner saw unexpected response: hdr=%v body=%q", probeResp.Header, string(probeBody))
	}

	// 4. 生成 VLESS-Reality 连接码并在客户端启动内置隧道
	pair, err := mgr.CreatePair()
	if err != nil {
		t.Fatalf("CreatePair: %v", err)
	}
	if !strings.HasPrefix(pair.PairCode, "vless://") || !strings.Contains(pair.QRSVG, "<svg") {
		t.Fatalf("unexpected pair output: %+v", pair)
	}

	tun, err := StartClientTunnel(pair.PairCode, "南宫问蛙的 iPhone")
	if err != nil {
		t.Fatalf("StartClientTunnel: %v", err)
	}
	defer tun.Close()

	cli := httpClientViaTunnel(tun)

	// 5. 访问 frog.internal API 与 SSE 事件流
	resp, err := cli.Get("http://frog.internal/api/devices")
	if err != nil {
		t.Fatalf("GET frog.internal/api/devices: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "蛙蛙服务器") {
		t.Fatalf("unexpected devices response: status=%d body=%s", resp.StatusCode, string(raw))
	}

	sseResp, err := cli.Get("http://frog.internal/api/bots/main/stream")
	if err != nil {
		t.Fatalf("GET SSE stream: %v", err)
	}
	sseBytes, _ := io.ReadAll(sseResp.Body)
	sseResp.Body.Close()
	if !strings.Contains(string(sseBytes), `"seq":3`) {
		t.Fatalf("SSE stream incomplete: %s", string(sseBytes))
	}

	// 6. 验证通过内置隧道直达主机内网其他端口（HTTP 代理 & SOCKS5 代理双模式）
	lanResp, err := cli.Get(lanSvc.URL)
	if err != nil {
		t.Fatalf("GET internal LAN service via HTTP proxy: %v", err)
	}
	lanBody, _ := io.ReadAll(lanResp.Body)
	lanResp.Body.Close()
	if string(lanBody) != "internal-lan-ok" {
		t.Fatalf("unexpected LAN body: %q", string(lanBody))
	}

	if err := checkSOCKS5Request(tun.Port(), lanSvc.Listener.Addr().String()); err != nil {
		t.Fatalf("SOCKS5 tunnel check failed: %v", err)
	}

	// 7. 验证设备已自动激活且记录设备名，吊销后立即阻断访问
	cur := mgr.Status()
	if len(cur.Devices) != 1 || cur.Devices[0].Name != "南宫问蛙的 iPhone" {
		t.Fatalf("expected 1 activated device named 南宫问蛙的 iPhone, got %+v", cur.Devices)
	}
	mgr.RevokeDevice(cur.Devices[0].ID)

	revokedResp, err := cli.Get("http://frog.internal/api/devices")
	if err == nil {
		body, _ := io.ReadAll(revokedResp.Body)
		revokedResp.Body.Close()
		if revokedResp.StatusCode == http.StatusOK {
			t.Fatalf("expected revoked device to be blocked, but got 200: %s", string(body))
		}
	}
}

func TestP2PUDPTunnelEndToEnd(t *testing.T) {
	lanSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("frog-p2p-payload-", 512)))
	}))
	defer lanSvc.Close()

	mgr, err := NewManager(t.TempDir(), newMockFrogApp())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	st, err := mgr.UpdateConfig(SaveInput{
		Mode:       ModeP2P,
		P2PListen:  "127.0.0.1:0",
		PublicHost: "127.0.0.1",
		ServerName: "家里电脑",
	})
	if err != nil {
		t.Fatalf("UpdateConfig p2p: %v", err)
	}
	if !st.Ready {
		t.Fatalf("expected P2P Ready=true, got %+v", st)
	}

	pair, err := mgr.CreatePair()
	if err != nil {
		t.Fatalf("CreatePair p2p: %v", err)
	}
	if !strings.HasPrefix(pair.PairCode, "frogp2p://") {
		t.Fatalf("expected frogp2p:// URI, got %s", pair.PairCode)
	}

	tun, err := StartClientTunnel(pair.PairCode, "iPhone P2P")
	if err != nil {
		t.Fatalf("StartClientTunnel p2p: %v", err)
	}
	defer tun.Close()

	cli := httpClientViaTunnel(tun)

	// 验证 API 与大数据块可靠传输（ARQ 分片重组 + Yamux 多路复用）
	resp, err := cli.Get("http://frog.internal/api/devices")
	if err != nil {
		t.Fatalf("P2P GET frog.internal/api/devices: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "蛙蛙服务器") {
		t.Fatalf("unexpected P2P devices response: %d %s", resp.StatusCode, string(raw))
	}

	bigResp, err := cli.Get(lanSvc.URL)
	if err != nil {
		t.Fatalf("P2P GET large LAN payload: %v", err)
	}
	bigData, _ := io.ReadAll(bigResp.Body)
	bigResp.Body.Close()
	if len(bigData) != len("frog-p2p-payload-")*512 {
		t.Fatalf("P2P large payload size mismatch: got %d", len(bigData))
	}

	// 吊销设备后 P2P 会话立即关闭
	cur := mgr.Status()
	if len(cur.Devices) != 1 || cur.Devices[0].Name != "iPhone P2P" {
		t.Fatalf("unexpected P2P devices: %+v", cur.Devices)
	}
	mgr.RevokeDevice(cur.Devices[0].ID)

	revResp, err := cli.Get("http://frog.internal/api/devices")
	if err == nil {
		revResp.Body.Close()
		if revResp.StatusCode == http.StatusOK {
			t.Fatalf("expected revoked P2P peer to fail, got 200")
		}
	}
}

func checkSOCKS5Request(proxyPort int, targetHostPort string) error {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	var authResp [2]byte
	if _, err := io.ReadFull(c, authResp[:]); err != nil || authResp != [2]byte{0x05, 0x00} {
		return fmt.Errorf("socks5 auth failed: %v %v", authResp, err)
	}

	host, portStr, _ := net.SplitHostPort(targetHostPort)
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], uint16(port))
	req = append(req, pb[:]...)
	if _, err := c.Write(req); err != nil {
		return err
	}

	var reply [10]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil || reply[1] != 0x00 {
		return fmt.Errorf("socks5 connect failed: %v %v", reply, err)
	}

	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+targetHostPort+"\r\nConnection: close\r\n\r\n"); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "internal-lan-ok" {
		return fmt.Errorf("unexpected socks5 body: %q", string(body))
	}
	return nil
}
