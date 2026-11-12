package bot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIGatewayTokensAndStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-up" {
			http.Error(w, "no", 401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"m1"`) {
			http.Error(w, "wrong model "+string(body), 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	dir := t.TempDir()
	b := newBot(t, dir, &fakeAI{})
	defer b.Close()
	key := "sk-up"
	if _, err := b.saveChannels(APISettings{Providers: []APIChannel{
		{ID: "up", Name: "Up", BaseURL: upstream.URL + "/v1", Enabled: true, AuthRequired: true, APIKey: &key, Models: []string{"m1"}},
		{ID: "off", Name: "Off", BaseURL: upstream.URL + "/v1", Enabled: false, Models: []string{"m1"}},
	}, DefaultProviderID: "up", Model: "m1"}); err != nil {
		t.Fatal(err)
	}
	g, err := startCLIGateway(b.pool)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, _, _, err := g.Issue("up", "nope", time.Minute); err == nil {
		t.Fatal("model outside the channel must be refused")
	}
	if _, _, _, err := g.Issue("off", "m1", time.Minute); err == nil {
		t.Fatal("disabled channel must be refused")
	}
	base, tok, revoke, err := g.Issue("up", "m1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	post := func(token string) (int, string) {
		req, _ := http.NewRequest("POST", base+"/chat/completions", strings.NewReader(`{"model":"x","stream":true,"messages":[]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := post(tok); code != 200 || !strings.Contains(body, `"content":"hi"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream: %d %s", code, body)
	}
	if code, _ := post("wrong"); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}
	revoke()
	if code, _ := post(tok); code != 401 {
		t.Fatalf("revoked token still works: %d", code)
	}
	_, short, _, _ := g.Issue("up", "m1", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if code, _ := post(short); code != 401 {
		t.Fatalf("expired token still works: %d", code)
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") || filepath.Base(base) != "v1" {
		t.Fatalf("gateway must listen on loopback only: %s", base)
	}
}
