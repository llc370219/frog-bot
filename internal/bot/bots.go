package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
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
const maxBots = 224 // OneFrog avatar slots

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
	mu       sync.RWMutex
	main     *Bot
	workRoot string
	profiles []BotProfile
	runtimes map[string]*Bot
	handlers map[string]*botEndpoint
	retired  sync.WaitGroup
	sharedMu sync.Mutex
	closed   bool
}

type botEndpoint struct {
	http.Handler
	requests sync.WaitGroup
}

var botIDPattern = regexp.MustCompile(`^bot_[a-f0-9]{16}$`)

func OpenBots(cfg Config) (*Bots, error) {
	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	main, err := New(cfg)
	if err != nil {
		return nil, err
	}
	bs := &Bots{main: main, workRoot: workspace + "-bots", profiles: []BotProfile{main.botProfile()},
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
			p.AvatarSlot < 0 || p.AvatarSlot >= maxBots || slots[p.AvatarSlot] {
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
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 8000 {
		return "", "", errors.New("消息不能为空且最多 8000 字节")
	}
	if fromID == toID {
		return "", "", errors.New("不能给自己发消息")
	}
	bs.mu.RLock()
	if bs.closed {
		bs.mu.RUnlock()
		return "", "", errors.New("Bot 管理器正在关闭")
	}
	recipient := bs.runtimes[toID]
	_, senderExists := bs.runtimes[fromID]
	bs.mu.RUnlock()
	if !senderExists || recipient == nil {
		return "", "", fmt.Errorf("发送方或接收方 Bot 不存在")
	}
	var original *Message
	if replyTo != "" {
		for _, m := range bs.runtimes[fromID].store.Inbox(true) {
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
		bs.runtimes[fromID].store.Append("message.state", map[string]string{"ID": original.ID, "State": "handled"}, true)
	}
	bs.runtimes[fromID].store.Append("message", Message{ID: id, Role: "notice", Text: text, FromBotID: fromID, ToBotID: toID, RequestID: requestID, ReplyTo: replyTo, MessageState: "sent", Hop: hop, Silent: true, CreatedAt: now}, true)
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

func (b *Bot) askAgent(to, question string) (string, error) {
	if b.coordinator == nil {
		return "", errors.New("当前未启用多 Bot")
	}
	_, requestID, err := b.coordinator.deliver(b.ID(), to, "请回答以下问题，并使用 message_agent 将答复发回请求方；reply_to 必须使用请求 ID。问题：\n"+question, "", "")
	if err != nil {
		return "", err
	}
	timer := time.NewTimer(75 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, m := range b.store.Inbox(true) {
			if m.ReplyTo == requestID && m.FromBotID == to {
				b.store.Append("message.state", map[string]string{"ID": m.ID, "State": "handled"}, true)
				return m.Text, nil
			}
		}
		select {
		case <-b.ctx.Done():
			return "", errors.New("Bot 已关闭")
		case <-timer.C:
			return "", errors.New("等待对方 Bot 回复超时（75 秒）；请求仍保留在对方收件箱中")
		case <-ticker.C:
		}
	}
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
	var wg sync.WaitGroup
	for _, b := range bs.runtimes {
		wg.Add(1)
		go func(b *Bot) { defer wg.Done(); b.Close() }(b)
	}
	wg.Wait()
	bs.retired.Wait()
}

func (bs *Bots) remove(id string) error {
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
	}()
	return nil
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
		used := [maxBots]bool{}
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
		b.Start()
		bs.runtimes[p.ID], bs.handlers[p.ID] = b, &botEndpoint{Handler: b.Handler("")}
		b.coordinator = bs
	}
	bs.profiles = next
	return p, nil
}

func (bs *Bots) Handler(webDir string, embeddedFS ...fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", bs.main.Handler(webDir, embeddedFS...))
	mux.HandleFunc("GET /api/bots", func(w http.ResponseWriter, r *http.Request) {
		bs.mu.RLock()
		profiles := slices.Clone(bs.profiles)
		bs.mu.RUnlock()
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, profiles, nil)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaType != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		var p BotProfile
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p); err != nil {
			writeJSON(w, nil, errors.New("请求格式不正确"))
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
	mux.HandleFunc("DELETE /api/bots/{id}", func(w http.ResponseWriter, r *http.Request) {
		err := bs.remove(r.PathValue("id"))
		if errors.Is(err, fs.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("/api/bots/{id}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		bs.mu.RLock()
		h := bs.handlers[r.PathValue("id")]
		if h == nil || bs.closed {
			bs.mu.RUnlock()
			http.NotFound(w, r)
			return
		}
		h.requests.Add(1)
		bs.mu.RUnlock()
		defer h.requests.Done()
		rest := r.PathValue("rest")
		if rest == "settings" || strings.HasPrefix(rest, "settings/") || rest == "update" {
			http.NotFound(w, r)
			return
		}
		scoped := r.Clone(r.Context())
		scoped.URL.Path, scoped.URL.RawPath = "/api/"+rest, ""
		h.ServeHTTP(w, scoped)
	})
	return mux
}
