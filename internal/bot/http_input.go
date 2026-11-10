package bot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

const maxJSONBody = 1 << 20

// decodeJSON validates the entire request before a handler performs any mutation.
// Unknown fields remain allowed for compatibility with newer clients.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		inputError(w, http.StatusUnsupportedMediaType, "请求必须使用 JSON")
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			inputError(w, http.StatusRequestEntityTooLarge, "请求内容太大")
		} else {
			inputError(w, http.StatusBadRequest, "无法读取请求内容")
		}
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, dst) != nil {
		inputError(w, http.StatusBadRequest, "请求必须是一个完整的 JSON 对象")
		return false
	}
	return true
}

func inputError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
