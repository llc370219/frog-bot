// Package gateway implements Module 7: Single-Pane Multiplexed WebSocket Gateway
// (controlling all bots across the FrogLink Mesh through one Home Node connection)
// and pluggable offline wake-up push notifications (FCM / UnifiedPush / Bark / Telegram / Webhook).
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"frogbot/internal/host"
	"frogbot/internal/vault"
)

// PushEvent describes an urgent offline wake-up alert sent when mobile/desktop clients are backgrounded.
type PushEvent struct {
	Kind      string            `json:"kind"` // "ASK_USER" | "WAITING_HUMAN" | "DENY" | "BUDGET_EXCEEDED" | "DIGEST"
	BotID     string            `json:"bot_id"`
	NodeID    string            `json:"node_id,omitempty"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	ActionID  string            `json:"action_id,omitempty"` // approval card ID or verify session ID
	Extra     map[string]string `json:"extra,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// OfflinePushNotifier dispatches wake-up notifications over pluggable channels
// (FCM / UnifiedPush / Bark / Telegram Bot / Webhook) whenever no active foreground client is online.
type OfflinePushNotifier struct {
	Vault      *vault.Vault
	HTTPClient *http.Client

	mu     sync.RWMutex
	cfg    host.PushConfig
	sent   []PushEvent // ring buffer of recent pushes for audit/testing
	onSend func(channel string, ev PushEvent)
}

func NewOfflinePushNotifier(cfg host.PushConfig, vlt *vault.Vault) *OfflinePushNotifier {
	return &OfflinePushNotifier{
		Vault:      vlt,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		cfg:        cfg,
	}
}

func (n *OfflinePushNotifier) UpdateConfig(cfg host.PushConfig) {
	n.mu.Lock()
	n.cfg = cfg
	n.mu.Unlock()
}

func (n *OfflinePushNotifier) Config() host.PushConfig {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.cfg
}

// RegisterMobileEndpoint registers a client's UnifiedPush or FCM token dynamically at runtime.
func (n *OfflinePushNotifier) RegisterMobileEndpoint(unifiedPushURL, fcmToken, barkURL string) {
	n.mu.Lock()
	if strings.TrimSpace(unifiedPushURL) != "" {
		n.cfg.UnifiedPushURL = strings.TrimSpace(unifiedPushURL)
	}
	if strings.TrimSpace(fcmToken) != "" {
		n.cfg.FCMDeviceToken = strings.TrimSpace(fcmToken)
	}
	if strings.TrimSpace(barkURL) != "" {
		n.cfg.BarkURL = strings.TrimSpace(barkURL)
	}
	n.mu.Unlock()
}

// Recent returns recent offline push alerts.
func (n *OfflinePushNotifier) Recent() []PushEvent {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return append([]PushEvent(nil), n.sent...)
}

func (n *OfflinePushNotifier) resolveSecret(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || n.Vault == nil {
		return ref
	}
	name := ref
	if strings.HasPrefix(name, "{{secret://") && strings.HasSuffix(name, "}}") {
		name = strings.TrimSuffix(strings.TrimPrefix(name, "{{secret://"), "}}")
	} else if strings.HasPrefix(name, "{{secret:") && strings.HasSuffix(name, "}}") {
		name = strings.TrimSuffix(strings.TrimPrefix(name, "{{secret:"), "}}")
	} else if strings.HasPrefix(name, "secret://") {
		name = strings.TrimPrefix(name, "secret://")
	} else {
		return ref
	}
	if vals, err := n.Vault.Values([]string{name}); err == nil {
		return vals[name]
	}
	return ""
}

// Notify delivers ev across all configured pluggable channels concurrently.
func (n *OfflinePushNotifier) Notify(ctx context.Context, ev PushEvent) error {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	n.mu.Lock()
	cfg := n.cfg
	n.sent = append(n.sent, ev)
	if len(n.sent) > 50 {
		n.sent = n.sent[len(n.sent)-50:]
	}
	hook := n.onSend
	n.mu.Unlock()

	var firstErr error
	recordErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// 1. Bark (iOS / macOS)
	if cfg.BarkURL != "" {
		if hook != nil {
			hook("bark", ev)
		}
		recordErr(n.sendBark(ctx, cfg.BarkURL, ev))
	}
	// 2. UnifiedPush (Android open push)
	if cfg.UnifiedPushURL != "" {
		if hook != nil {
			hook("unifiedpush", ev)
		}
		recordErr(n.sendJSONPost(ctx, cfg.UnifiedPushURL, "", ev))
	}
	// 3. FCM (Android)
	if cfg.FCMDeviceToken != "" && cfg.FCMServerKeyRef != "" {
		if hook != nil {
			hook("fcm", ev)
		}
		key := n.resolveSecret(cfg.FCMServerKeyRef)
		if key != "" {
			payload := map[string]any{
				"to": cfg.FCMDeviceToken,
				"notification": map[string]string{
					"title": ev.Title,
					"body":  ev.Body,
				},
				"data": ev,
			}
			recordErr(n.sendJSONPost(ctx, "https://fcm.googleapis.com/fcm/send", "key="+key, payload))
		}
	}
	// 4. Telegram Bot fallback
	if cfg.TelegramTokenRef != "" && cfg.TelegramChatID != "" {
		if hook != nil {
			hook("telegram", ev)
		}
		tok := n.resolveSecret(cfg.TelegramTokenRef)
		if tok != "" {
			tgURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(tok))
			payload := map[string]any{
				"chat_id": cfg.TelegramChatID,
				"text":    fmt.Sprintf("🐸 [%s] %s\n%s", ev.Kind, ev.Title, ev.Body),
			}
			recordErr(n.sendJSONPost(ctx, tgURL, "", payload))
		}
	}
	// 5. Generic Webhook
	if cfg.WebhookURL != "" {
		if hook != nil {
			hook("webhook", ev)
		}
		recordErr(n.sendJSONPost(ctx, cfg.WebhookURL, "", ev))
	}
	return firstErr
}

func (n *OfflinePushNotifier) sendBark(ctx context.Context, baseURL string, ev PushEvent) error {
	payload := map[string]any{
		"title": ev.Title,
		"body":  ev.Body,
		"group": "frogbot-" + strings.ToLower(ev.Kind),
		"level": barkLevel(ev.Kind),
	}
	return n.sendJSONPost(ctx, strings.TrimRight(baseURL, "/"), "", payload)
}

func barkLevel(kind string) string {
	switch kind {
	case "DENY", "ASK_USER", "WAITING_HUMAN":
		return "timeSensitive"
	default:
		return "active"
	}
}

func (n *OfflinePushNotifier) sendJSONPost(ctx context.Context, endpoint, authHeader string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := n.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("push endpoint %s returned HTTP %d", endpoint, resp.StatusCode)
	}
	return nil
}
