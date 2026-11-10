package bot

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONRequestsValidateBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	handler := b.Handler("")
	for _, tc := range []struct {
		name, body, media string
		status            int
	}{
		{"truncated", `{"text":"do not save",`, "application/json", 400},
		{"trailing-object", `{"text":"do not save"} {}`, "application/json", 400},
		{"trailing-garbage", `{"text":"do not save"} garbage`, "application/json", 400},
		{"null", "null", "application/json", 400},
		{"array", "[]", "application/json", 400},
		{"wrong-type", `{"text":42}`, "application/json", 400},
		{"form", `{"text":"do not save"}`, "text/plain", 415},
		{"too-large", `{"text":` + `"` + strings.Repeat("a", maxJSONBody) + `"}`, "application/json", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/messages", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.media)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			var problem map[string]string
			if json.Unmarshal(w.Body.Bytes(), &problem) != nil || problem["error"] == "" {
				t.Fatal("missing JSON error")
			}
			if len(b.store.Messages()) != 0 {
				t.Fatal("invalid request mutated history")
			}
		})
	}
	req := httptest.NewRequest("POST", "/api/messages", strings.NewReader(`{"text":"valid", "future_field":true}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 || len(b.store.Messages()) != 1 {
		t.Fatalf("valid client request rejected: %d %s", w.Code, w.Body)
	}
}

func TestMultipartFailureRollsBackCreatedFiles(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "first.txt")
	_, _ = part.Write([]byte("complete first file"))
	part, _ = writer.CreateFormFile("file", "second.txt")
	_, _ = part.Write([]byte("second file"))
	_ = writer.Close()
	// The first part is complete, but the second is missing its closing boundary.
	raw := body.Bytes()[:body.Len()-len(writer.Boundary())-8]
	req := httptest.NewRequest("POST", "/api/uploads", bytes.NewReader(raw))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	b.Handler("").ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("truncated upload returned %d: %s", w.Code, w.Body)
	}
	entries, err := os.ReadDir(filepath.Join(b.cfg.Workspace, "uploads"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial uploads left behind: %v, %v", entries, err)
	}
}

func TestEmptyUploadRejectedAndValidUploadAccepted(t *testing.T) {
	dir := t.TempDir()
	b, err := New(Config{DataDir: filepath.Join(dir, "data"), Workspace: filepath.Join(dir, "workspace")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, withFile := range []bool{false, true} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if withFile {
			part, _ := writer.CreateFormFile("file", "hello.txt")
			_, _ = part.Write([]byte("hello"))
		}
		_ = writer.Close()
		req := httptest.NewRequest("POST", "/api/uploads", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		w := httptest.NewRecorder()
		b.Handler("").ServeHTTP(w, req)
		want := 400
		if withFile {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("withFile=%v: %d: %s", withFile, w.Code, w.Body)
		}
	}
}
