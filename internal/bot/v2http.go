package bot

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frogbot/internal/clipool"
	"frogbot/internal/host"
	"frogbot/internal/mesh"
	"frogbot/internal/ops"
	"frogbot/internal/organs"
	"frogbot/internal/remote"
	"frogbot/internal/security"
)

// mountV2Routes registers all Frog Bot V2 HTTP & Multiplexed WebSocket endpoints under /api/v2/*.
func (bs *Bots) mountV2Routes(mux *http.ServeMux) {
	if bs.v2 == nil {
		return
	}
	hr := bs.v2

	// 1. Single-Pane Multiplexed WebSocket Gateway + Built-in Zero-Knowledge WSS Blind Relay (443-compatible!)
	mux.HandleFunc("GET /api/v2/ws", hr.WS.ServeWS)
	wssRelay := remote.NewWSSBlindRelay(os.Getenv("FROGBOT_RELAY_TOKEN"))
	mux.HandleFunc("GET /api/v2/relay/ws", wssRelay.ServeWS)

	// 2. Host V2 Status & Host Doctor
	mux.HandleFunc("GET /api/v2/status", func(w http.ResponseWriter, r *http.Request) {
		budgets := map[string]host.BudgetStatus{}
		bs.mu.RLock()
		for id, b := range bs.runtimes {
			if b.v2 != nil && b.v2.Budget != nil {
				budgets[id] = b.v2.Budget.Status()
			}
		}
		bs.mu.RUnlock()
		writeJSON(w, map[string]any{
			"version":               currentVersion(),
			"node_id":               hr.Mesh.Identity.NodeID,
			"node_name":             hr.Config.NodeName,
			"network_tag":           hr.Config.NetworkTag,
			"ws_clients":            hr.WS.ClientCount(),
			"active_leases":         hr.Leases.Active(),
			"budgets":               budgets,
			"cli_quotas":            hr.Quota.Snapshot(),
			"cli_tasks":             hr.CLIPool.List(""),
			"pending_approvals":     hr.SecurityGW.Pending(),
			"pending_verifications": hr.VerifyMgr.List("WAITING_HUMAN"),
			"peers":                 hr.Mesh.ListPeersStatus(),
			"static_peer_directory": hr.Mesh.StaticDirectory(),
			"browser_active":        hr.BrowserPool.ActiveCount(),
		}, nil)
	})

	mux.HandleFunc("GET /api/v2/doctor", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fix") == "1" {
			_ = hr.Doctor.FixPermissions()
		}
		writeJSON(w, hr.Doctor.Diagnose(r.Context()), nil)
	})

	// 3. Independent Security Gateway Approvals (supports allow_once / allow_whitelist / deny)
	mux.HandleFunc("GET /api/v2/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, hr.SecurityGW.Pending(), nil)
	})
	mux.HandleFunc("POST /api/v2/approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body security.UserApprovalChoice
		if !decodeJSON(w, r, &body, 8<<10) {
			return
		}
		if body.DecidedBy == "" {
			body.DecidedBy = "user"
		}
		err := hr.SecurityGW.Resolve(r.PathValue("id"), body)
		writeJSON(w, map[string]bool{"ok": err == nil}, err)
	})

	// 4. LocalCLIPool Tasks, Live Logs & Passive Quota Manager
	mux.HandleFunc("GET /api/v2/cli/quotas", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, hr.Quota.Snapshot(), nil)
	})
	mux.HandleFunc("GET /api/v2/cli/tasks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, hr.CLIPool.List(r.URL.Query().Get("bot_id")), nil)
	})
	mux.HandleFunc("POST /api/v2/cli/tasks", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BotID         string `json:"bot_id"`
			Mode          string `json:"mode"`
			CLIPreference string `json:"cli_preference"`
			TaskPrompt    string `json:"task_prompt"`
			TimeoutSec    int    `json:"timeout_sec"`
		}
		if !decodeJSON(w, r, &body, 128<<10) {
			return
		}
		if body.BotID == "" {
			body.BotID = defaultBotID
		}
		b, rel := bs.acquireBot(body.BotID)
		defer rel()
		if b == nil {
			http.NotFound(w, r)
			return
		}
		logsDir := filepath.Join(b.cfg.DataDir, "logs")
		if b.v2 != nil {
			logsDir = b.v2.Layout.LogsDir
		}
		sock := ""
		if hr.ExecBridge != nil {
			sock = hr.ExecBridge.SocketPath
		}
		st, err := hr.CLIPool.Dispatch(hr.ctx, clipool.CLITaskRequest{
			BotID:          body.BotID,
			Mode:           body.Mode,
			CLIPreference:  body.CLIPreference,
			TaskPrompt:     body.TaskPrompt,
			Workspace:      b.cfg.Workspace,
			LogsDir:        logsDir,
			TimeoutSec:     body.TimeoutSec,
			Priority:       clipool.PriorityUser,
			ExecSocketPath: sock,
		})
		writeJSON(w, st, err)
	})
	mux.HandleFunc("GET /api/v2/cli/tasks/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		content, nextOff, err := hr.CLIPool.ReadLog(r.PathValue("id"), off)
		writeJSON(w, map[string]any{"task_id": r.PathValue("id"), "content": content, "offset": nextOff}, err)
	})
	mux.HandleFunc("POST /api/v2/cli/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		err := hr.CLIPool.Cancel(r.PathValue("id"))
		writeJSON(w, map[string]bool{"ok": err == nil}, err)
	})

	// 5. Ready-to-Verify Remote Captcha / Login Handshake
	mux.HandleFunc("GET /api/v2/verify", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, hr.VerifyMgr.List(r.URL.Query().Get("status")), nil)
	})
	mux.HandleFunc("POST /api/v2/verify/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BotID          string `json:"bot_id"`
			TaskID         string `json:"task_id"`
			URL            string `json:"url"`
			ScriptPath     string `json:"script_path"`
			Reason         string `json:"reason"`
			ScreenshotPath string `json:"screenshot_path"`
		}
		if !decodeJSON(w, r, &body, 32<<10) {
			return
		}
		if body.BotID == "" {
			body.BotID = defaultBotID
		}
		b, rel := bs.acquireBot(body.BotID)
		defer rel()
		profileDir, ws := "", ""
		if b != nil {
			ws = b.cfg.Workspace
			if b.v2 != nil {
				profileDir = b.v2.Layout.BrowserProfileDir
			}
		}
		sess := hr.VerifyMgr.RegisterWaitingHuman(body.BotID, body.TaskID, body.URL, body.ScriptPath, profileDir, ws, body.Reason, body.ScreenshotPath)
		writeJSON(w, sess, nil)
	})
	mux.HandleFunc("POST /api/v2/verify/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		err := hr.VerifyMgr.StartLiveSession(hr.ctx, r.PathValue("id"), hr.WS.PushVerifyFrame)
		writeJSON(w, map[string]bool{"ok": err == nil}, err)
	})
	mux.HandleFunc("POST /api/v2/verify/{id}/input", func(w http.ResponseWriter, r *http.Request) {
		var ev organs.VerifyInputEvent
		if !decodeJSON(w, r, &ev, 16<<10) {
			return
		}
		err := hr.VerifyMgr.DispatchInput(r.PathValue("id"), ev)
		writeJSON(w, map[string]bool{"ok": err == nil}, err)
	})
	mux.HandleFunc("POST /api/v2/verify/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		sess, err := hr.VerifyMgr.CompleteSession(r.PathValue("id"))
		writeJSON(w, sess, err)
	})
	mux.HandleFunc("POST /api/v2/verify/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		sess, _ := hr.VerifyMgr.StopLiveSession(r.PathValue("id"), "CANCELLED")
		writeJSON(w, sess, nil)
	})

	// 6. FrogLink Mesh (Ed25519 Pairing, E2EE Delivery & Public NAT Relay Tunnel)
	mux.HandleFunc("GET /api/v2/mesh/peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"node_id":          hr.Mesh.Identity.NodeID,
			"network_tag":      hr.Config.NetworkTag,
			"static_directory": hr.Mesh.StaticDirectory(),
			"peers":            hr.Mesh.ListPeersStatus(),
		}, nil)
	})
	mux.HandleFunc("POST /api/v2/mesh/pair-code", func(w http.ResponseWriter, r *http.Request) {
		inv := hr.Mesh.CreatePairCode(10 * time.Minute)
		writeJSON(w, inv, nil)
	})
	mux.HandleFunc("POST /api/v2/mesh/pair-accept", func(w http.ResponseWriter, r *http.Request) {
		var req mesh.PairAcceptRequest
		if !decodeJSON(w, r, &req, 32<<10) {
			return
		}
		rec, err := hr.Mesh.AcceptPairRequest(req)
		writeJSON(w, rec, err)
	})
	mux.HandleFunc("POST /api/v2/mesh/deliver", func(w http.ResponseWriter, r *http.Request) {
		var env mesh.EncryptedEnvelope
		if !decodeJSON(w, r, &env, 256<<10) {
			return
		}
		err := hr.Mesh.DeliverLocalEnvelope(env)
		writeJSON(w, map[string]bool{"ok": err == nil}, err)
	})
	mux.HandleFunc("GET /api/v2/mesh/tunnel", hr.Mesh.ServeTunnelHTTP)

	// 7. Offline Wake-up Push Config & Mobile Token Registration
	mux.HandleFunc("GET /api/v2/push/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, hr.Push.Config(), nil)
	})
	mux.HandleFunc("POST /api/v2/push/config", func(w http.ResponseWriter, r *http.Request) {
		var cfg host.PushConfig
		if !decodeJSON(w, r, &cfg, 16<<10) {
			return
		}
		hr.Push.UpdateConfig(cfg)
		hr.Config.Push = cfg
		_ = host.SaveHostConfig(hr.Layout.HostConfigPath(), hr.Config)
		writeJSON(w, cfg, nil)
	})
	mux.HandleFunc("POST /api/v2/push/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UnifiedPushURL string `json:"unified_push_url"`
			FCMDeviceToken string `json:"fcm_device_token"`
			BarkURL        string `json:"bark_url"`
		}
		if !decodeJSON(w, r, &body, 8<<10) {
			return
		}
		hr.Push.RegisterMobileEndpoint(body.UnifiedPushURL, body.FCMDeviceToken, body.BarkURL)
		hr.Config.Push = hr.Push.Config()
		_ = host.SaveHostConfig(hr.Layout.HostConfigPath(), hr.Config)
		writeJSON(w, map[string]bool{"ok": true}, nil)
	})

	// 8. Disk GC & One-Click Encrypted Asset Backup / Export
	mux.HandleFunc("POST /api/v2/gc", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, hr.GC.RunNow(), nil)
	})
	mux.HandleFunc("POST /api/v2/backup/export", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Passphrase string `json:"passphrase"`
		}
		if !decodeJSON(w, r, &body, 4<<10) {
			return
		}
		tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("frogbot-export-%d.frogbak", time.Now().UnixNano()))
		defer os.Remove(tmpFile)
		if _, err := ops.Backup(bs.main.cfg.DataDir, tmpFile, body.Passphrase); err != nil {
			writeJSON(w, nil, err)
			return
		}
		f, err := os.Open(tmpFile)
		if err != nil {
			writeJSON(w, nil, err)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="frogbot-backup-%s.frogbak"`, time.Now().Format("20060102-150405")))
		_, _ = io.Copy(w, f)
	})

	// 9. Per-Bot Hermes Memory, Skills & Budget Endpoints
	mux.HandleFunc("GET /api/v2/bots/{id}/memory", func(w http.ResponseWriter, r *http.Request) {
		b, rel := bs.acquireBot(r.PathValue("id"))
		defer rel()
		if b == nil || b.v2 == nil || b.v2.Hermes == nil {
			http.NotFound(w, r)
			return
		}
		uEntries, uUsage, _ := b.v2.Hermes.UserBlock.List()
		mEntries, mUsage, _ := b.v2.Hermes.MemoryBlock.List()
		writeJSON(w, map[string]any{
			"user":          map[string]any{"usage": uUsage, "entries": uEntries},
			"memory":        map[string]any{"usage": mUsage, "entries": mEntries},
			"history_count": b.v2.Hermes.History.Count(r.Context()),
			"dirty":         b.v2.Hermes.SnapshotDirty(),
		}, nil)
	})
	mux.HandleFunc("POST /api/v2/bots/{id}/memory", func(w http.ResponseWriter, r *http.Request) {
		b, rel := bs.acquireBot(r.PathValue("id"))
		defer rel()
		if b == nil || b.v2 == nil || b.v2.Hermes == nil {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Action  string `json:"action"`
			Target  string `json:"target"`
			EntryID string `json:"entry_id"`
			Content string `json:"content"`
		}
		if !decodeJSON(w, r, &body, 16<<10) {
			return
		}
		res, err := b.v2.Hermes.ManageMemory(body.Action, body.Target, body.EntryID, body.Content)
		writeJSON(w, res, err)
	})
	mux.HandleFunc("GET /api/v2/bots/{id}/skills", func(w http.ResponseWriter, r *http.Request) {
		b, rel := bs.acquireBot(r.PathValue("id"))
		defer rel()
		if b == nil || b.v2 == nil || b.v2.Hermes == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, b.v2.Hermes.Skills.List(), nil)
	})
	mux.HandleFunc("GET /api/v2/bots/{id}/budget", func(w http.ResponseWriter, r *http.Request) {
		b, rel := bs.acquireBot(r.PathValue("id"))
		defer rel()
		if b == nil || b.v2 == nil || b.v2.Budget == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, b.v2.Budget.Status(), nil)
	})
	mux.HandleFunc("POST /api/v2/bots/{id}/budget/unpause", func(w http.ResponseWriter, r *http.Request) {
		b, rel := bs.acquireBot(r.PathValue("id"))
		defer rel()
		if b == nil || b.v2 == nil || b.v2.Budget == nil {
			http.NotFound(w, r)
			return
		}
		b.v2.Budget.UnpauseBackground()
		writeJSON(w, b.v2.Budget.Status(), nil)
	})
	mux.HandleFunc("GET /api/v2/bots/{id}/digest", func(w http.ResponseWriter, r *http.Request) {
		b, rel := bs.acquireBot(r.PathValue("id"))
		defer rel()
		if b == nil || b.v2 == nil || b.v2.Digest == nil {
			http.NotFound(w, r)
			return
		}
		if strings.EqualFold(r.URL.Query().Get("flush"), "1") {
			brief, cnt, err := b.v2.Digest.Flush("manual")
			writeJSON(w, map[string]any{"briefing": brief, "count": cnt}, err)
			return
		}
		writeJSON(w, b.v2.Digest.Pending(), nil)
	})

	// 9. Official CLI Remote Headless Login (Codex / Claude / Gemini / Grok — strictly separate from reverse proxy)
	if hr.CLIAuth != nil {
		mux.HandleFunc("GET /api/v2/cli/auth", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, hr.CLIAuth.ListStatuses(), nil)
		})
		mux.HandleFunc("POST /api/v2/cli/auth/{cli}/start", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Mode string `json:"mode"`
			}
			_ = decodeJSON(w, r, &body, 4<<10)
			sess, err := hr.CLIAuth.StartLogin(r.Context(), r.PathValue("cli"), body.Mode)
			writeJSON(w, sess, err)
		})
		mux.HandleFunc("GET /api/v2/cli/auth/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
			sess, ok := hr.CLIAuth.GetSession(r.PathValue("id"))
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, sess, nil)
		})
		mux.HandleFunc("POST /api/v2/cli/auth/sessions/{id}/input", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input string `json:"input"`
			}
			if !decodeJSON(w, r, &body, 16<<10) {
				return
			}
			sess, err := hr.CLIAuth.SubmitInput(r.Context(), r.PathValue("id"), body.Input)
			writeJSON(w, sess, err)
		})
		mux.HandleFunc("POST /api/v2/cli/auth/sessions/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
			err := hr.CLIAuth.CancelSession(r.PathValue("id"))
			writeJSON(w, map[string]bool{"ok": err == nil}, err)
		})
		mux.HandleFunc("POST /api/v2/cli/auth/{cli}/credential", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			if !decodeJSON(w, r, &body, 32<<10) {
				return
			}
			st, err := hr.CLIAuth.SaveCredential(r.PathValue("cli"), body.Kind, body.Value)
			writeJSON(w, st, err)
		})
		mux.HandleFunc("POST /api/v2/cli/auth/{cli}/logout", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				ClearNative bool `json:"clear_native"`
			}
			_ = decodeJSON(w, r, &body, 4<<10)
			st, err := hr.CLIAuth.Logout(r.PathValue("cli"), body.ClearNative)
			writeJSON(w, st, err)
		})
	}

	// 10. Optional Front-Bot Reverse Proxy Gateway (CLIProxyAPI / OpenCodex / Magpie / Custom) with mandatory disclaimer
	if hr.ProxyGW != nil {
		mux.HandleFunc("GET /api/v2/proxy/status", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, hr.ProxyGW.Status(r.Context()), nil)
		})
		mux.HandleFunc("POST /api/v2/proxy/config", func(w http.ResponseWriter, r *http.Request) {
			var body ProxyGatewaySaveInput
			if !decodeJSON(w, r, &body, 64<<10) {
				return
			}
			st, err := hr.ProxyGW.Save(r.Context(), body)
			writeJSON(w, st, err)
		})
		mux.HandleFunc("POST /api/v2/proxy/start", func(w http.ResponseWriter, r *http.Request) {
			st, err := hr.ProxyGW.Start(r.Context())
			writeJSON(w, st, err)
		})
		mux.HandleFunc("POST /api/v2/proxy/stop", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, hr.ProxyGW.Stop(r.Context()), nil)
		})
		mux.HandleFunc("POST /api/v2/proxy/sync", func(w http.ResponseWriter, r *http.Request) {
			err := hr.ProxyGW.SyncChannel(r.Context())
			writeJSON(w, hr.ProxyGW.Status(r.Context()), err)
		})
	}
}
