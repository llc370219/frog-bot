package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frogbot/internal/agy"
	"frogbot/internal/clipool"
	"frogbot/internal/gateway"
	"frogbot/internal/host"
	"frogbot/internal/memory"
	"frogbot/internal/mesh"
	"frogbot/internal/onefrog"
	"frogbot/internal/ops"
	"frogbot/internal/organs"
	"frogbot/internal/security"
	"frogbot/internal/vault"
)

// HostRuntime aggregates all Frog Bot V2 Host Daemon subsystems shared across all Bot Actors.
type HostRuntime struct {
	Layout      host.Layout
	Config      host.HostConfig
	Doctor      *host.HostDoctor
	Leases      *host.LeaseManager
	Vault       *vault.Vault
	SecurityGW  *security.SecurityGateway
	ExecBridge  *security.ExecBridge
	Quota       *clipool.QuotaManager
	CLIPool     *clipool.LocalCLIPool
	CLIAuth     *clipool.CLIAuthManager
	ProxyGW     *ProxyGatewayManager
	BrowserPool *organs.BrowserPool
	VerifyMgr   *organs.ReadyToVerifyManager
	Mesh        *mesh.FrogLinkMesh
	Push        *gateway.OfflinePushNotifier
	WS          *gateway.SinglePaneGateway
	GC          *ops.DiskGC

	ctx    context.Context
	cancel context.CancelFunc
}

// BotV2 holds per-Bot Actor V2 extensions (Hermes memory/FTS5/skills/pointer,
// BudgetGuard, TaintTracker, sequential Actor inbox, DigestQueue, and optional IMAP funnel).
type BotV2 struct {
	Layout      host.BotLayout
	Config      host.BotConfig
	Hermes      *memory.HermesManager
	Budget      *host.BudgetGuard
	Taint       *security.TaintTracker
	Actor       *host.BotActor
	Digest      *organs.DigestQueue
	IMAP        *organs.IMAPFunnel
	archivedMu  sync.Mutex
	archivedIDs map[string]bool
}

func openHostRuntime(bs *Bots) (*HostRuntime, error) {
	layout := host.NewLayout(bs.main.cfg.DataDir)
	if err := layout.EnsureHostDirs(); err != nil {
		return nil, fmt.Errorf("init host dirs: %w", err)
	}
	hcfg, err := host.LoadHostConfig(layout.HostConfigPath())
	if err != nil {
		hcfg = host.DefaultHostConfig()
	}

	// Open Host Blind Vault (vault.enc) using the host master key (0600)
	key, _, err := vault.LoadKey(bs.main.cfg.MasterKey, bs.main.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	hostVault, err := vault.OpenFile(layout.VaultPath(), key)
	if err != nil {
		hostVault = bs.main.vault
	}

	leases := host.NewLeaseManager()
	push := gateway.NewOfflinePushNotifier(hcfg.Push, hostVault)
	secGW := security.NewSecurityGateway(hostVault, nil)
	quota := clipool.NewQuotaManager(hcfg.CLIFallbackChain, hcfg.CLIMaxConcurrent, hcfg.CLIReservePercent)
	selfBin, _ := os.Executable()
	cliPool := clipool.NewLocalCLIPool(quota, leases, hostVault, clipool.IsolationOptions{
		WorkerUser:    hcfg.WorkerUser,
		ProtectedDirs: []string{layout.HostDir, bs.main.cfg.DataDir},
		SelfBinary:    selfBin,
		PreferBwrap:   true,
	})
	cliAuth := clipool.NewCLIAuthManager(hostVault)
	cliPool.ExtraEnv = cliAuth.EnvOverrides
	agy.SetExtraEnvProvider(cliAuth.EnvOverrides)
	proxyGW := NewProxyGatewayManager(layout.HostDir, hostVault, bs.main.pool)
	browserPool := organs.NewBrowserPool(leases, time.Duration(hcfg.BrowserIdleSec)*time.Second)
	verifyMgr := organs.NewReadyToVerifyManager(browserPool, cliPool, leases)

	m, err := mesh.OpenMesh(layout.HostDir, hcfg.NodeName, hcfg.NetworkTag, "")
	if err != nil {
		return nil, fmt.Errorf("init FrogLink Mesh: %w", err)
	}
	m.LocalBotsFn = func() []string {
		bs.mu.RLock()
		defer bs.mu.RUnlock()
		out := make([]string, 0, len(bs.profiles))
		for _, p := range bs.profiles {
			out = append(out, p.ID)
		}
		return out
	}
	m.LocalQuotaFn = quota.Snapshot

	ctx, cancel := context.WithCancel(context.Background())
	hr := &HostRuntime{
		Layout:      layout,
		Config:      hcfg,
		Doctor:      host.NewHostDoctor(layout, hcfg),
		Leases:      leases,
		Vault:       hostVault,
		SecurityGW:  secGW,
		Quota:       quota,
		CLIPool:     cliPool,
		CLIAuth:     cliAuth,
		ProxyGW:     proxyGW,
		BrowserPool: browserPool,
		VerifyMgr:   verifyMgr,
		Mesh:        m,
		Push:        push,
		GC: ops.NewDiskGC(bs.main.cfg.DataDir, ops.GCConfig{
			LogMaxAge:   time.Duration(hcfg.GCRetentionDays) * 24 * time.Hour,
			MaxLogBytes: hcfg.GCMaxLogBytes,
		}, bs.main.cfg.Workspace, bs.workRoot),
		ctx:    ctx,
		cancel: cancel,
	}

	wsGW := gateway.NewSinglePaneGateway(gateway.HostCallbacks{
		NodeID: m.Identity.NodeID,
		ListTopology: func() any {
			return hr.topologySnapshot(bs)
		},
		SendChatMessage: func(_ context.Context, botID, text string) (any, error) {
			b, rel := bs.acquireBot(botID)
			defer rel()
			if b == nil {
				return nil, fmt.Errorf("Bot %s 不存在", botID)
			}
			return b.Send(text)
		},
		SecurityGateway: secGW,
		CLIPool:         cliPool,
		VerifyManager:   verifyMgr,
		Mesh:            m,
		PushNotifier:    push,
	})
	hr.WS = wsGW

	// Wire cross-module event hooks
	secGW.OnApprovalCard = wsGW.PushApprovalCard
	secGW.OnDenyAlert = wsGW.PushDenyAlert
	verifyMgr.OnNotifyHuman = wsGW.PushVerifyWaiting
	verifyMgr.OnFrame = wsGW.PushVerifyFrame
	cliPool.OnLogChunk = wsGW.PushCLILogChunk
	cliPool.OnComplete = func(cb clipool.CLITaskCallback) {
		wsGW.Broadcast("cli.task.done", cb)
		b, rel := bs.acquireBot(cb.BotID)
		defer rel()
		if b == nil {
			return
		}
		filesStr := "无"
		if len(cb.ChangedFiles) > 0 {
			filesStr = strings.Join(cb.ChangedFiles, ", ")
		}
		msg := fmt.Sprintf("Coding CLI 任务 %s（%s/%s）已结束，状态：%s，变更文件：%s。\n结论摘要：\n%s\n完整日志：%s",
			cb.TaskID, cb.CLI, cb.Mode, cb.Status, filesStr, cb.Summary, cb.LogPath)
		if b.v2 != nil && b.v2.Actor != nil {
			_ = b.v2.Actor.Submit(host.ActorEvent{
				Type:    host.EventTaskCallback,
				BotID:   cb.BotID,
				Source:  "cli:" + cb.TaskID,
				Text:    msg,
				Payload: cb,
			})
		} else {
			b.notify(msg)
		}
	}

	m.OnGatewayProxy = wsGW.HandleMeshProxy
	m.OnPeerMessage = func(pm mesh.PeerMessage) {
		botID, _, ok := mesh.ParseBotAddress(pm.ToBot)
		if !ok {
			botID = defaultBotID
		}
		b, rel := bs.acquireBot(botID)
		defer rel()
		if b == nil {
			b, rel = bs.acquireBot(defaultBotID)
			defer rel()
		}
		if b == nil {
			return
		}
		sources := append([]string(nil), pm.TaintSources...)
		sources = append(sources, "peer:"+pm.FromBot)
		text := fmt.Sprintf("[跨服 FrogLink 消息] 来自 %s | 意图: %s | 需要回复: %t | 污点标记: %t\n内容：\n%s",
			pm.FromBot, pm.Intent, pm.NeedReply, pm.Tainted, pm.Payload)
		if b.v2 != nil && b.v2.Actor != nil {
			_ = b.v2.Actor.Submit(host.ActorEvent{
				Type:         host.EventPeerMessage,
				BotID:        b.ID(),
				Source:       "peer:" + pm.FromBot,
				Text:         text,
				Payload:      pm,
				Tainted:      pm.Tainted || true, // cross-node payload defaults tainted unless explicitly trusted
				TaintSources: sources,
			})
		} else {
			b.notify(text)
		}
	}

	// Start frog-exec Unix socket bridge (best-effort if socket path exceeds OS sun_path limit in deep test tempdirs)
	sockPath := layout.ExecSocketPath()
	if len(sockPath) > 96 {
		sockPath = filepath.Join(os.TempDir(), fmt.Sprintf("frogexec-%s.sock", m.Identity.NodeID))
	}
	hr.ExecBridge = security.NewExecBridge(sockPath, hostVault, secGW, func(botID string) (string, bool) {
		b, rel := bs.acquireBot(botID)
		defer rel()
		if b == nil {
			return "", false
		}
		return b.cfg.Workspace, true
	})
	if err := hr.ExecBridge.Start(ctx); err != nil {
		log.Printf("⚠ frog-exec 桥监听未启动（%s）: %v", sockPath, err)
	}

	// Start daily disk GC loop & passive CLI local usage estimator refresh
	hr.GC.StartDailyLoop(ctx, nil)
	go func() {
		quota.RefreshLocalEstimates()
		tk := time.NewTicker(10 * time.Minute)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				quota.RefreshLocalEstimates()
			}
		}
	}()

	return hr, nil
}

func (hr *HostRuntime) Close() {
	if hr == nil {
		return
	}
	hr.cancel()
	if hr.ExecBridge != nil {
		_ = hr.ExecBridge.Close()
	}
	if hr.CLIPool != nil {
		hr.CLIPool.Close()
	}
	if hr.ProxyGW != nil {
		hr.ProxyGW.Close()
	}
	if hr.BrowserPool != nil {
		hr.BrowserPool.Close()
	}
	if hr.Mesh != nil {
		hr.Mesh.Close()
	}
}

func (hr *HostRuntime) topologySnapshot(bs *Bots) map[string]any {
	bs.mu.RLock()
	profiles := append([]BotProfile(nil), bs.profiles...)
	bs.mu.RUnlock()
	return map[string]any{
		"node_id":     hr.Mesh.Identity.NodeID,
		"node_name":   hr.Config.NodeName,
		"network_tag": hr.Config.NetworkTag,
		"local_bots":  profiles,
		"peers":       hr.Mesh.ListPeersStatus(),
	}
}

func initBotV2(b *Bot, hr *HostRuntime) error {
	if hr == nil {
		return nil
	}
	bl := hr.Layout.BotDirs(b.ID(), b.cfg.DataDir, b.cfg.Workspace)
	if err := bl.EnsureBotDirs(); err != nil {
		return err
	}
	bcfg, err := host.LoadBotConfig(bl.ConfigPath, b.cfg.Name)
	if err != nil {
		bcfg = host.DefaultBotConfig(b.cfg.Name)
	}

	// Stateless light-model summarizer for FTS5 history & >1200-char pointerization
	lightSum := func(ctx context.Context, text string) (string, error) {
		prov, model := b.resolveModel(true)
		if prov == nil || model == "" {
			return "", fmt.Errorf("no model")
		}
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		msg, err := prov.Chat(sctx, onefrog.ChatRequest{
			Model: model,
			Messages: []onefrog.Message{
				{Role: "system", Content: "请用一句简洁的中文（不超过 120 字）提炼以下内容的核心事实摘要，不要开场白。"},
				{Role: "user", Content: text},
			},
		}, nil)
		if err != nil {
			return "", err
		}
		return msg.Content, nil
	}

	hermes, err := memory.NewHermesManager(
		hr.Layout.UserMemoryPath(),
		bl.MemoryPath,
		bl.SoulPath,
		bl.SkillsDir,
		bl.HistoryDBPath,
		bl.WorkspaceDir,
		lightSum,
	)
	if err != nil {
		return err
	}

	budget, err := host.NewBudgetGuard(b.ID(), bl.BudgetPath, bcfg.Budget, bcfg.Timezone)
	if err != nil {
		_ = hermes.Close()
		return err
	}
	budget.OnTrip = func(st host.BudgetStatus) {
		if hr.WS != nil {
			hr.WS.Broadcast("alert.budget", st)
		}
		if hr.Push != nil {
			_ = hr.Push.Notify(context.Background(), gateway.PushEvent{
				Kind:   "BUDGET_EXCEEDED",
				BotID:  b.ID(),
				NodeID: hr.Mesh.Identity.NodeID,
				Title:  "API 每日预算触发熔断",
				Body:   st.PausedReason,
			})
		}
	}

	digest, _ := organs.OpenDigestQueue(filepath.Join(bl.BaseDir, "digest_queue.json"))
	taint := security.NewTaintTracker()

	actor := host.NewBotActor(b.ID(), 256, func(_ context.Context, ev host.ActorEvent) error {
		if ev.Tainted {
			for _, src := range ev.TaintSources {
				taint.Mark("event", src)
			}
			if len(ev.TaintSources) == 0 {
				taint.Mark("event", ev.Source)
			}
		}
		switch ev.Type {
		case host.EventUserMessage:
			_, _ = b.Send(ev.Text)
		case host.EventTaskCallback, host.EventPeerMessage, host.EventApprovalResult:
			if strings.TrimSpace(ev.Text) != "" {
				b.notify(ev.Text)
			}
		}
		return nil
	})
	actor.Start(b.ctx)

	var imapFunnel *organs.IMAPFunnel
	if bcfg.IMAP != nil && bcfg.IMAP.Enabled {
		imapFunnel = organs.NewIMAPFunnel(b.ID(), *bcfg.IMAP, hr.Vault, hermes.Pointer, budget, actor)
		imapFunnel.Start(b.ctx)
	}

	b.v2 = &BotV2{
		Layout:      bl,
		Config:      bcfg,
		Hermes:      hermes,
		Budget:      budget,
		Taint:       taint,
		Actor:       actor,
		Digest:      digest,
		IMAP:        imapFunnel,
		archivedIDs: map[string]bool{},
	}
	// Rebuild chat tools registry so V2 tools are mounted once b.v2 is attached
	b.chatTool = onefrog.NewRegistry(b.chatTools()...)
	return nil
}

func (bv *BotV2) Close() {
	if bv == nil {
		return
	}
	if bv.IMAP != nil {
		bv.IMAP.Stop()
	}
	if bv.Actor != nil {
		bv.Actor.Close()
	}
	if bv.Hermes != nil {
		_ = bv.Hermes.Close()
	}
}

func (b *Bot) hostRuntime() *HostRuntime {
	if b.coordinator != nil {
		return b.coordinator.v2
	}
	return nil
}

// archiveEvictedToFTS5 asynchronously archives older chat messages outside the 10-round window into SQLite FTS5 trigram.
func (b *Bot) archiveEvictedToFTS5(ctx context.Context, all []Message, kept []Message) {
	if b.v2 == nil || b.v2.Hermes == nil || b.v2.Hermes.History == nil {
		return
	}
	keptSet := make(map[string]bool, len(kept))
	for _, m := range kept {
		keptSet[m.ID] = true
	}
	var toArchive []memory.HistoryItem
	b.v2.archivedMu.Lock()
	for _, m := range all {
		if keptSet[m.ID] || m.Hidden || b.v2.archivedIDs[m.ID] {
			continue
		}
		txt := strings.TrimSpace(msgText(m))
		if txt == "" {
			continue
		}
		b.v2.archivedIDs[m.ID] = true
		toArchive = append(toArchive, memory.HistoryItem{
			ID:        m.ID,
			Role:      m.Role,
			Content:   b.vault.Redact(txt),
			CreatedAt: m.CreatedAt,
		})
	}
	b.v2.archivedMu.Unlock()
	if len(toArchive) > 0 {
		b.v2.Hermes.History.ArchiveAsync(ctx, toArchive)
	}
}

func encodePrettyJSON(v any) string {
	raw, _ := json.MarshalIndent(v, "", "  ")
	return string(raw)
}
