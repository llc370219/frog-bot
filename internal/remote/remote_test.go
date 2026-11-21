package remote

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dummyApp() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"id": "local", "name": "测试主机", "online": true, "bots": []map[string]any{{"id": "main", "name": "青蛙"}}},
		})
	})
	mux.HandleFunc("GET /api/bots/main/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"kind\":\"replay.done\",\"data\":{\"busy\":false}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	return mux
}

func TestDirectTLSAndPairFlow(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir, dummyApp())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	st, err := mgr.UpdateConfig(SaveInput{
		Mode:         ModeDirect,
		DirectListen: "127.0.0.1:0",
		PublicHost:   "127.0.0.1",
		ServerName:   "测试Mac",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Ready || st.TLSPin == "" || !strings.HasPrefix(st.EndpointURL, "https://127.0.0.1:") {
		t.Fatalf("unexpected status: %+v", st)
	}

	pair, err := mgr.CreatePair()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pair.PairCode, "frogpair://connect?") || !strings.Contains(pair.QRSVG, "<svg") {
		t.Fatalf("unexpected pair: %+v", pair)
	}

	u, err := url.Parse(pair.PairCode)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := u.Query().Get("u")
	tok := u.Query().Get("t")
	pin := u.Query().Get("fp")

	tlsCfg, err := NewPinnedTLSConfig(pin, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}

	// 1. 扫描器访问错误路径应返回 404
	rootBase, _ := url.Parse(endpoint)
	resp404, err := client.Get(rootBase.Scheme + "://" + rootBase.Host + "/api/devices")
	if err != nil {
		t.Fatal(err)
	}
	resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on unprefixed path, got %d", resp404.StatusCode)
	}

	// 2. 未配对访问正确前缀应返回 401
	resp401, err := client.Get(endpoint + "/api/devices")
	if err != nil {
		t.Fatal(err)
	}
	resp401.Body.Close()
	if resp401.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 before pair, got %d", resp401.StatusCode)
	}

	// 3. 兑换配对码
	body, _ := json.Marshal(map[string]string{"token": tok, "device_name": "测试iPhone"})
	respPair, err := client.Post(endpoint+"/api/remote/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer respPair.Body.Close()
	if respPair.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(respPair.Body)
		t.Fatalf("pair failed (%d): %s", respPair.StatusCode, string(raw))
	}
	var pairRes struct {
		Session  string `json:"session"`
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(respPair.Body).Decode(&pairRes); err != nil {
		t.Fatal(err)
	}

	// 4. 同一配对码二次使用必须失败
	respReplay, err := client.Post(endpoint+"/api/remote/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	respReplay.Body.Close()
	if respReplay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected one-time token rejection, got %d", respReplay.StatusCode)
	}

	// 5. 携带设备会话访问 API
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/api/devices", nil)
	req.AddCookie(&http.Cookie{Name: "frogbot_session", Value: pairRes.Session})
	respOK, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	respOK.Body.Close()
	if respOK.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after pair, got %d", respOK.StatusCode)
	}

	// 6. 移除设备授权后立即失效
	mgr.RevokeDevice(pairRes.DeviceID)
	respRevoked, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	respRevoked.Body.Close()
	if respRevoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 after revoke, got %d", respRevoked.StatusCode)
	}
}

func TestBridgeReverseTunnelAndSSE(t *testing.T) {
	bridgeDir := t.TempDir()
	cert, fp, err := EnsureCert(filepath.Join(bridgeDir, "b.crt"), filepath.Join(bridgeDir, "b.key"))
	if err != nil {
		t.Fatal(err)
	}
	bridgeToken := randomHex(24)
	bridgeLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeLn.Close()

	bridgeSv := &http.Server{Handler: NewBridgeServer(bridgeToken).Handler()}
	defer bridgeSv.Close()
	go func() { _ = bridgeSv.Serve(bridgeLn) }()

	_, port, _ := net.SplitHostPort(bridgeLn.Addr().String())
	bridgeURI := fmt.Sprintf("frog-bridge://127.0.0.1:%s#%s.%s", port, bridgeToken, fp)

	// 内网侧主机完全不开监听端口，主动连接 Bridge
	homeDir := t.TempDir()
	mgr, err := NewManager(homeDir, dummyApp())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	if _, err := mgr.UpdateConfig(SaveInput{
		Mode:       ModeBridge,
		BridgeURI:  bridgeURI,
		ServerName: "家里Mac",
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.Status().Ready {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !mgr.Status().Ready {
		t.Fatalf("bridge tunnel did not become ready: %+v", mgr.Status())
	}

	pair, err := mgr.CreatePair()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(pair.PairCode)
	endpoint := u.Query().Get("u")
	tok := u.Query().Get("t")
	pin := u.Query().Get("fp")

	tlsCfg, err := NewPinnedTLSConfig(pin, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}

	body, _ := json.Marshal(map[string]string{"token": tok, "device_name": "5G手机"})
	respPair, err := client.Post(endpoint+"/api/remote/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var pairRes struct {
		Session string `json:"session"`
	}
	_ = json.NewDecoder(respPair.Body).Decode(&pairRes)
	respPair.Body.Close()
	if pairRes.Session == "" {
		t.Fatal("empty session over bridge")
	}

	// 验证通过反向隧道读取 SSE 实时事件流
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/api/bots/main/stream", nil)
	req.AddCookie(&http.Cookie{Name: "frogbot_session", Value: pairRes.Session})
	respSSE, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer respSSE.Body.Close()
	sc := bufio.NewScanner(respSSE.Body)
	if !sc.Scan() || !strings.Contains(sc.Text(), "replay.done") {
		t.Fatalf("expected SSE line through bridge, got %q (err=%v)", sc.Text(), sc.Err())
	}
}
