package bot

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV2FullStackEndpoints(t *testing.T) {
	tmp := t.TempDir()
	bs, err := OpenBots(Config{
		Name:      "FrogV2",
		DataDir:   tmp + "/data",
		Workspace: tmp + "/ws",
	})
	if err != nil {
		t.Fatalf("OpenBots: %v", err)
	}
	defer bs.Close()

	h := bs.Handler(nil)
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var r *http.Request
		if body != nil {
			raw, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(raw))
			r.Header.Set("Content-Type", "application/json")
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. /api/v2/status
	if w := do("GET", "/api/v2/status", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"node_id"`) {
		t.Fatalf("GET /api/v2/status failed: %d %s", w.Code, w.Body.String())
	}

	// 2. /api/v2/doctor
	if w := do("GET", "/api/v2/doctor", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"sqlite_fts5"`) {
		t.Fatalf("GET /api/v2/doctor failed: %d %s", w.Code, w.Body.String())
	}

	// 3. /api/v2/bots/main/memory add & list
	if w := do("POST", "/api/v2/bots/main/memory", map[string]string{
		"action": "add", "target": "user", "content": "回复简洁",
	}); w.Code != 200 {
		t.Fatalf("POST /api/v2/bots/main/memory failed: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/api/v2/bots/main/memory", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "回复简洁") {
		t.Fatalf("GET /api/v2/bots/main/memory failed: %d %s", w.Code, w.Body.String())
	}

	// 4. /api/v2/mesh/pair-code
	if w := do("POST", "/api/v2/mesh/pair-code", map[string]any{}); w.Code != 200 || !strings.Contains(w.Body.String(), "FROG-") {
		t.Fatalf("POST /api/v2/mesh/pair-code failed: %d %s", w.Code, w.Body.String())
	}

	// 5. /api/v2/verify/register
	if w := do("POST", "/api/v2/verify/register", map[string]string{
		"bot_id": "main", "url": "https://checkin.test", "reason": "需要滑块验证码",
	}); w.Code != 200 || !strings.Contains(w.Body.String(), "WAITING_HUMAN") {
		t.Fatalf("POST /api/v2/verify/register failed: %d %s", w.Code, w.Body.String())
	}

	// 6. /api/v2/gc & /api/v2/backup/export
	if w := do("POST", "/api/v2/gc", map[string]any{}); w.Code != 200 {
		t.Fatalf("POST /api/v2/gc failed: %d %s", w.Code, w.Body.String())
	}
	if w := do("POST", "/api/v2/backup/export", map[string]string{"passphrase": "TestPass#2026"}); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "FROGBK02") {
		t.Fatalf("POST /api/v2/backup/export failed: %d len=%d", w.Code, w.Body.Len())
	}
}
