package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"frogbot/internal/onefrog"
)

const defaultBotID = "main"

// 最多 32 个 Bot：每个 Bot 都有自己的聊天循环、事件日志和后台任务队列，再多对本机/小服务器没有意义。
// 头像库有 224 个位置，新 Bot 取最小的空位。
const (
	maxBots     = 32
	avatarSlots = 224
)

type BotProfile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	AvatarSlot   int    `json:"avatarSlot"`
}

func (b *Bot) botProfile() BotProfile {
	if p := b.profile.Load(); p != nil {
		return *p
	}
	return BotProfile{ID: defaultBotID, Name: b.cfg.Name}
}

// Bots owns one runtime per profile. Keep the original runtime and its paths
// for the default Bot so existing data and API clients keep working.
type Bots struct {
	mu         sync.RWMutex
	main       *Bot
	workRoot   string
	profiles   []BotProfile
	runtimes   map[string]*Bot
	handlers   map[string]*botEndpoint
	retired    sync.WaitGroup
	sharedMu   sync.Mutex
	deliveryMu sync.Mutex
	network    *peerNetwork
	closed     bool
}

type botEndpoint struct {
	http.Handler
	requests sync.WaitGroup
}

var botIDPattern = regexp.MustCompile(`^bot_[a-f0-9]{16}$`)

func OpenBots(cfg Config) (*Bots, error) {
	network, err := newPeerNetwork(cfg.Devices)
	if err != nil {
		return nil, err
	}
	cfg.Devices = nil // peer credentials stay with the device network
	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	main, err := New(cfg)
	if err != nil {
		return nil, err
	}
	bs := &Bots{main: main, workRoot: workspace + "-bots", profiles: []BotProfile{main.botProfile()}, network: network,
		runtimes: map[string]*Bot{defaultBotID: main}, handlers: map[string]*botEndpoint{defaultBotID: {Handler: main.Handler("")}}}
	main.coordinator = bs
	raw, err := os.ReadFile(bs.path())
	if err == nil {
		err = json.Unmarshal(raw, &bs.profiles)
		if err == nil {
			err = validateProfiles(bs.profiles)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		bs.Close()
		return nil, fmt.Errorf("读取 Bot 列表: %w", err)
	}
	for _, p := range bs.profiles {
		if p.ID == defaultBotID {
			main.profile.Store(&p)
			continue
		}
		b, err := bs.open(p)
		if err != nil {
			bs.Close()
			return nil, fmt.Errorf("打开 Bot %s: %w", p.ID, err)
		}
		bs.runtimes[p.ID], bs.handlers[p.ID] = b, &botEndpoint{Handler: b.Handler("")}
		b.coordinator = bs
	}
	for _, b := range bs.runtimes {
		b.Start()
	}
	return bs, nil
}

func validateProfiles(profiles []BotProfile) error {
	if len(profiles) == 0 || len(profiles) > maxBots || profiles[0].ID != defaultBotID {
		return errors.New("Bot 列表缺少默认 Bot 或数量超限")
	}
	ids, slots := map[string]bool{}, map[int]bool{}
	for _, p := range profiles {
		if ids[p.ID] || (p.ID != defaultBotID && !botIDPattern.MatchString(p.ID)) ||
			p.AvatarSlot < 0 || p.AvatarSlot >= avatarSlots || slots[p.AvatarSlot] {
			return errors.New("Bot 列表含无效或重复的 ID 或头像")
		}
		ids[p.ID], slots[p.AvatarSlot] = true, true
	}
	return nil
}

func (bs *Bots) open(p BotProfile) (*Bot, error) {
	cfg := bs.main.cfg
	cfg.Name = p.Name
	cfg.DataDir = filepath.Join(cfg.DataDir, "bots", p.ID)
	cfg.Workspace = filepath.Join(bs.workRoot, p.ID)
	return newRuntime(cfg, bs.main, &p)
}

func (bs *Bots) path() string { return filepath.Join(bs.main.cfg.DataDir, "bots.json") }

func (bs *Bots) sharedMemoryPath() string {
	return filepath.Join(bs.main.cfg.DataDir, "shared-memory.md")
}

func (bs *Bots) readSharedMemory() (string, error) {
	bs.sharedMu.Lock()
	defer bs.sharedMu.Unlock()
	data, err := os.ReadFile(bs.sharedMemoryPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}

func (bs *Bots) writeSharedMemory(text, mode string) error {
	bs.sharedMu.Lock()
	defer bs.sharedMu.Unlock()
	return bs.main.writeMemoryFile(bs.sharedMemoryPath(), text, mode)
}

// Deliver stores the message in the recipient's event log before waking its chat loop.
func (bs *Bots) Deliver(fromID, toID, text, requestID, replyTo string) (string, error) {
	id, _, err := bs.deliver(fromID, toID, text, requestID, replyTo)
	return id, err
}

func (bs *Bots) deliver(fromID, toID, text, requestID, replyTo string) (string, string, error) {
	if strings.Contains(toID, "/") {
		if requestID != "" {
			return "", "", errors.New("request_id 由系统生成，发送方不能指定")
		}
		return bs.deliverPeer(fromID, toID, text, replyTo, false)
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 8000 {
		return "", "", errors.New("消息不能为空且最多 8000 字节")
	}
	if fromID == toID {
		return "", "", errors.New("不能给自己发消息")
	}
	sender, releaseSender := bs.acquireBot(fromID)
	defer releaseSender()
	recipient, releaseRecipient := bs.acquireBot(toID)
	defer releaseRecipient()
	if sender == nil || recipient == nil {
		return "", "", fmt.Errorf("发送方或接收方 Bot 不存在")
	}
	bs.deliveryMu.Lock()
	defer bs.deliveryMu.Unlock()
	var original *Message
	if replyTo != "" {
		for _, m := range sender.store.Inbox(true) {
			if m.RequestID == replyTo && m.FromBotID == toID && m.ToBotID == fromID {
				copy := m
				original = &copy
				break
			}
		}
		if original == nil || original.MessageState != "pending" {
			return "", "", errors.New("reply_to 未匹配到该 Bot 发来的待处理请求")
		}
		if original.Hop >= 2 {
			return "", "", errors.New("Bot 消息最多允许一轮请求和一轮回复")
		}
		requestID = replyTo
	} else if requestID != "" {
		return "", "", errors.New("request_id 由系统生成，发送方不能指定")
	}
	if requestID == "" {
		requestID = onefrog.NewID("req")
	}
	id := onefrog.NewID("msg")
	now := time.Now()
	hop := 1
	if replyTo != "" {
		hop = 2
		sender.store.Append("message.state", map[string]string{"ID": original.ID, "State": "handled"}, true)
	}
	sender.store.Append("message", Message{ID: id, Role: "notice", Text: text, FromBotID: fromID, ToBotID: toID, RequestID: requestID, ReplyTo: replyTo, MessageState: "sent", Hop: hop, Silent: true, CreatedAt: now}, true)
	recipient.store.Append("message", Message{ID: id, Role: "notice", Text: text, FromBotID: fromID, ToBotID: toID, RequestID: requestID, ReplyTo: replyTo, MessageState: "pending", Hop: hop, CreatedAt: now}, false)
	recipient.wake()
	return id, requestID, nil
}

func (b *Bot) messageAgent(to, text, replyTo string) (string, error) {
	if b.coordinator == nil {
		return "", errors.New("当前未启用多 Bot")
	}
	return b.coordinator.Deliver(b.ID(), to, text, "", replyTo)
}

// askAgent 向另一个 Bot 提问，但不在当前这轮聊天里干等：请求写进对方的持久收件箱并唤醒对方，
// 立刻返回 request_id。对方用 message_agent(reply_to=request_id) 回复后，回复会作为收件箱消息
// 送到本 Bot 并唤醒聊天层，那时再转告用户。这样两个 Bot 同时互相提问也不会互相卡住。
func (b *Bot) askAgent(to, question string) (string, error) {
	if b.coordinator == nil {
		return "", errors.New("当前未启用多 Bot")
	}
	var requestID string
	var err error
	if strings.Contains(to, "/") {
		_, requestID, err = b.coordinator.deliverPeer(b.ID(), to, question, "", true)
	} else {
		_, requestID, err = b.coordinator.deliver(b.ID(), to, "请回答以下问题，并使用 message_agent 将答复发回请求方；reply_to 必须使用请求 ID。问题：\n"+question, "", "")
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已把问题发给 Bot %s（request_id=%s）。对方回复后会作为收件箱消息送到你这里并唤醒你，"+
		"届时再把答复转告用户。本轮不要等待，也不要重复提问；可以先告诉用户已经去问了。", to, requestID), nil
}

func (bs *Bots) save(profiles []BotProfile) error {
	tmp, err := os.CreateTemp(bs.main.cfg.DataDir, "bots-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(profiles)
	closeErr := tmp.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), bs.path())
}

func (bs *Bots) Close() {
	bs.mu.Lock()
	if bs.closed {
		bs.mu.Unlock()
		return
	}
	bs.closed = true
	bs.mu.Unlock()
	if bs.network != nil {
		bs.network.client.CloseIdleConnections()
		bs.network.settingsClient.CloseIdleConnections()
	}
	var wg sync.WaitGroup
	for _, b := range bs.runtimes {
		wg.Add(1)
		go func(b *Bot) { defer wg.Done(); b.Close() }(b)
	}
	wg.Wait()
	bs.retired.Wait()
}

// remove 删除 Bot。purge=false 时只从列表里移除，数据目录（聊天记录、记忆、密钥）和工作区都保留在磁盘上；
// purge=true 时在 Bot 停下之后把 data/bots/<id> 和它的工作区一起删掉，无法恢复。
func (bs *Bots) remove(id string, purge bool) error {
	if id == defaultBotID {
		return errors.New("默认 Bot 不能删除")
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.closed {
		return errors.New("服务正在关闭")
	}
	idx := slices.IndexFunc(bs.profiles, func(p BotProfile) bool { return p.ID == id })
	if idx < 0 {
		return fs.ErrNotExist
	}
	next := slices.Delete(slices.Clone(bs.profiles), idx, idx+1)
	if err := bs.save(next); err != nil {
		return err
	}
	b, endpoint := bs.runtimes[id], bs.handlers[id]
	bs.profiles = next
	delete(bs.runtimes, id)
	delete(bs.handlers, id)
	// No new requests can enter this endpoint. Stop its work and close its
	// stores after the requests that already entered have finished.
	bs.retired.Add(1)
	go func() {
		defer bs.retired.Done()
		b.stop()
		endpoint.requests.Wait()
		b.Close()
		if purge {
			bs.purge(b)
		}
	}()
	return nil
}

// purge 删除一个已关闭 Bot 的数据目录和工作区。路径都由系统生成（bot_ + 16 位十六进制），
// 这里再确认一次它们确实在 data/bots/ 和工作区根目录下面，防止误删。
func (bs *Bots) purge(b *Bot) {
	id := b.ID()
	if !botIDPattern.MatchString(id) {
		return
	}
	for _, p := range []struct{ dir, root string }{
		{b.cfg.DataDir, filepath.Join(bs.main.cfg.DataDir, "bots")},
		{b.cfg.Workspace, bs.workRoot},
	} {
		dir, err1 := filepath.Abs(p.dir)
		root, err2 := filepath.Abs(p.root)
		if err1 != nil || err2 != nil || filepath.Dir(dir) != root || filepath.Base(dir) != id {
			log.Printf("没有删除 %s：路径不在预期位置", p.dir)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("删除 %s 失败：%v", dir, err)
		}
	}
}

// botDataPaths 返回 Bot 的数据目录和工作区（界面在删除确认里展示）。
func (bs *Bots) botDataPaths(id string) (data, workspace string) {
	return absPath(filepath.Join(bs.main.cfg.DataDir, "bots", id)), absPath(filepath.Join(bs.workRoot, id))
}

func (bs *Bots) setProfile(id string, p BotProfile) (BotProfile, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Instructions = strings.TrimSpace(p.Instructions)
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 60 || utf8.RuneCountInString(p.Instructions) > 4000 {
		return BotProfile{}, errors.New("名称须为 1–60 字，职责说明最多 4000 字")
	}
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.closed {
		return BotProfile{}, errors.New("服务正在关闭")
	}
	next := slices.Clone(bs.profiles)
	if id != "" {
		idx := slices.IndexFunc(next, func(x BotProfile) bool { return x.ID == id })
		if idx < 0 {
			return BotProfile{}, fs.ErrNotExist
		}
		p.ID, p.AvatarSlot = id, next[idx].AvatarSlot
		next[idx] = p
		if err := bs.save(next); err != nil {
			return BotProfile{}, err
		}
		bs.runtimes[id].profile.Store(&p)
	} else {
		if len(next) >= maxBots {
			return BotProfile{}, errors.New("Bot 数量已达上限")
		}
		p.ID = onefrog.NewID("bot")
		used := [avatarSlots]bool{}
		for _, bot := range next {
			used[bot.AvatarSlot] = true
		}
		p.AvatarSlot = slices.Index(used[:], false)
		b, err := bs.open(p)
		if err != nil {
			return BotProfile{}, err
		}
		next = append(next, p)
		if err := bs.save(next); err != nil {
			b.Close()
			return BotProfile{}, err
		}
		bs.runtimes[p.ID], bs.handlers[p.ID] = b, &botEndpoint{Handler: b.Handler("")}
		b.coordinator = bs
		b.Start()
	}
	bs.profiles = next
	return p, nil
}

// Handler 是整个程序的 HTTP 入口：/api/bots/{id}/… 转给对应 Bot，/api/settings 等是全局设置，
// 其余路径提供前端页面（ui 为 nil 时只提供 API）。访问控制（本机限制或登录）由调用方在外面包一层。
func (bs *Bots) Handler(ui fs.FS) http.Handler {
	mux := http.NewServeMux()
	mainAPI := bs.main.Handler("")
	if ui != nil {
		mux.Handle("/", spa(ui))
	}
	// 全局接口（设置、更新检查）挂在默认 Bot 上；其他 /api/… 只能通过 /api/bots/{id}/… 访问。
	for _, p := range []string{"/api/settings", "/api/settings/", "/api/channels/", "POST /api/channels", "GET /api/update"} {
		mux.Handle(p, mainAPI)
	}
	mux.HandleFunc("GET /api/channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, bs.channelsView(), nil)
	})
	mux.HandleFunc("/api/", http.NotFound)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "version": currentVersion(), "api_version": 1}, nil)
	})
	mux.HandleFunc("GET /api/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"api_version": 1, "version": currentVersion(),
			"clients": []string{"web", "android", "macos"},
			"auth":    map[string]any{"session": "cookie", "status_path": "/api/auth/me"},
			"events":  map[string]any{"transport": "sse", "resume": "Last-Event-ID", "heartbeat_sec": 20},
			"limits": map[string]any{"json_bytes": maxJSONBody, "upload_file_bytes": maxUpload,
				"upload_request_bytes": maxUpload + (1 << 20), "upload_files": 16, "bots": maxBots},
		}, nil)
	})
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"enabled": false}, nil) // 本机模式没有登录；服务器模式由登录中间件接管这个地址
	})
	mux.HandleFunc("GET /api/bots", func(w http.ResponseWriter, r *http.Request) {
		bs.mu.RLock()
		profiles := slices.Clone(bs.profiles)
		bs.mu.RUnlock()
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, profiles, nil)
	})
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, bs.deviceGroups(r.Context()), nil)
	})
	mux.HandleFunc("POST /api/devices/{device}/bots", bs.proxyPeerCreateBot)
	mux.HandleFunc("/api/devices/{device}/bots/{bot}/{rest...}", bs.proxyPeerBot)
	bs.mountDeviceSettings(mux, "/api/devices/{device}", true)
	save := func(w http.ResponseWriter, r *http.Request) {
		var p BotProfile
		if !decodeJSON(w, r, &p, 64<<10) {
			return
		}
		p, err := bs.setProfile(r.PathValue("id"), p)
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, p, err)
	}
	mux.HandleFunc("POST /api/bots", save)
	mux.HandleFunc("PATCH /api/bots/{id}", save)
	mux.HandleFunc("GET /api/bots/{id}/paths", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		bs.mu.RLock()
		_, ok := bs.runtimes[id]
		bs.mu.RUnlock()
		if !ok || id == defaultBotID {
			http.NotFound(w, r)
			return
		}
		data, ws := bs.botDataPaths(id)
		writeJSON(w, map[string]string{"data_dir": data, "workspace": ws}, nil)
	})
	mux.HandleFunc("DELETE /api/bots/{id}", func(w http.ResponseWriter, r *http.Request) {
		err := bs.remove(r.PathValue("id"), r.URL.Query().Get("purge") == "1")
		if errors.Is(err, fs.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("POST /api/bots/{id}/settings/copy", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			From string `json:"from"`
		}
		if !decodeJSON(w, r, &body, 1024) {
			return
		}
		saved, err := bs.copySettings(body.From, r.PathValue("id"))
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, saved, err)
	})
	mux.HandleFunc("/api/bots/{id}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		rest := r.PathValue("rest")
		if rest == "update" {
			http.NotFound(w, r)
			return
		}
		bs.serveBotRoute(w, r, r.PathValue("id"), rest, false)
	})
	return mux
}

func (bs *Bots) serveBotRoute(w http.ResponseWriter, r *http.Request, id, rest string, peer bool) {
	b, endpoint, release := bs.acquireBotEndpoint(id)
	defer release()
	if endpoint == nil {
		http.NotFound(w, r)
		return
	}
	if peer && rest == "stream" {
		b.streamEvents(w, r, peerChatEvent)
		return
	}
	scoped := r.Clone(r.Context())
	scoped.URL.Path, scoped.URL.RawPath = "/api/"+rest, ""
	endpoint.ServeHTTP(w, scoped)
}

// copySettings 把一个 Bot 的模型选择（聊天模型、Agent 模型与执行方式）复制给另一个 Bot。
// API 渠道是整台设备共用的，不需要复制。复制之后两边各自独立。
func (bs *Bots) copySettings(fromID, toID string) (APISettings, error) {
	if fromID == toID {
		return APISettings{}, errors.New("请选择另一个 Bot 作为来源")
	}
	src, releaseSrc := bs.acquireBot(fromID)
	defer releaseSrc()
	dst, releaseDst := bs.acquireBot(toID)
	defer releaseDst()
	if src == nil || dst == nil {
		return APISettings{}, fs.ErrNotExist
	}
	src.modelMu.RLock()
	draft := APISettings{DefaultProviderID: src.defaultChannel, Model: src.chatModel,
		AgentProviderID: src.agentChannel, AgentModel: src.agentModelName}
	conn := src.connection
	src.modelMu.RUnlock()
	if !validRoute(dst.pool.public(), draft.DefaultProviderID, draft.Model) {
		draft.DefaultProviderID, draft.Model = "", ""
	}
	if !validRoute(dst.pool.public(), draft.AgentProviderID, draft.AgentModel) {
		draft.AgentProviderID, draft.AgentModel = draft.DefaultProviderID, draft.Model
	}
	if _, err := dst.saveRoutes(draft); err != nil {
		return APISettings{}, fmt.Errorf("复制模型选择失败: %w", err)
	}
	if err := dst.saveAgentConnection(conn); err != nil {
		return APISettings{}, fmt.Errorf("模型已复制，但 Agent 执行方式没有复制成功: %w", err)
	}
	return dst.GetSettings(), nil
}

// channelsView 列出设备上的 API 渠道，并附上每个 Bot 的聊天 / Agent 各用了哪个渠道。
func (bs *Bots) channelsView() channelsView {
	bs.mu.RLock()
	profiles := slices.Clone(bs.profiles)
	bots := make([]*Bot, len(profiles))
	for i, p := range profiles {
		bots[i] = bs.runtimes[p.ID]
	}
	bs.mu.RUnlock()
	view := channelsView{Providers: bs.main.pool.public(), Usage: []channelUse{}}
	for i, b := range bots {
		if b == nil {
			continue
		}
		b.modelMu.RLock()
		use := channelUse{BotID: profiles[i].ID, Name: profiles[i].Name, ChatProviderID: b.defaultChannel, AgentCLI: b.connection.CLI}
		if b.connection.CLI == "" {
			use.AgentProviderID = b.agentChannel
		}
		b.modelMu.RUnlock()
		view.Usage = append(view.Usage, use)
	}
	return view
}
