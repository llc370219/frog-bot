package agy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// nativeSessionInfo 表示从宿主机原生 Coding CLI（Claude Code / Codex / Antigravity）
// 磁盘存储或原生列表接口发现的历史会话元数据。
type nativeSessionInfo struct {
	ID         string
	Agent      string // "claude" | "codex" | "agy"
	NativeID   string // 原生 CLI 会话/线程 ID（用于 --resume / thread/resume）
	Cwd        string // 清理后的工作目录（适合显示和作为 cmd.Dir）
	RawCwd     string // 原生接口返回的原始 cwd 字符串（如 agy session/list）
	Title      string
	Model      string
	UpdatedAt  time.Time
	SourcePath string // 磁盘 JSONL 路径（claude / codex）
}

type nativeFileCacheEntry struct {
	size    int64
	modUnix int64
	valid   bool
	info    nativeSessionInfo
}

var (
	nativeCacheMu    sync.Mutex
	nativeFileCache  = map[string]nativeFileCacheEntry{}
	codexTitleCache  = map[string]codexTitleCacheEntry{}
)

type codexTitleCacheEntry struct {
	size    int64
	modUnix int64
	titles  map[string]string
}

const (
	nativeMetaHeadLimit = 4 << 20   // 元数据扫描读取文件头上限（4 MiB，避免首行大快照截断）
	nativeMetaTailLimit = 64 * 1024 // 元数据扫描读取文件尾上限（用于 custom-title / summary）
	nativeMetaMaxLines  = 1200
	nativeLineScanLimit = 4 << 20   // 元数据扫描单行上限 4 MiB（超长行跳过不中断）
	nativeFullLineLimit = 32 << 20  // 完整历史加载单行上限 32 MiB
	maxCodexScanFiles   = 500
	maxHistoryMessages  = 200
)

// ---------- 跨平台路径归一化（兼容 Windows 大小写、正反斜杠、\\?\ 与 file:/// URI） ----------

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func looksLikeWindowsPath(s string) bool {
	if len(s) >= 2 && isASCIIAlpha(s[0]) && s[1] == ':' {
		return true
	}
	if strings.HasPrefix(s, `\\`) || strings.HasPrefix(s, `//?/`) {
		return true
	}
	return false
}

func stripPathDecorators(p string) string {
	s := strings.TrimSpace(p)
	if s == "" {
		return ""
	}
	// 处理 file:///C:/... 或 file:///c%3A/... 或 file://localhost/...
	hadFileScheme := false
	if len(s) >= 7 && strings.EqualFold(s[:7], "file://") {
		hadFileScheme = true
		rest := s[7:]
		if len(rest) >= 10 && strings.EqualFold(rest[:10], "localhost/") {
			rest = rest[9:]
		}
		if unesc, err := url.PathUnescape(rest); err == nil {
			rest = unesc
		}
		s = rest
	}
	// 部分 ACP/CLI（如 Antigravity agy session/list）即使不带 file:// 前缀也会返回 URL 编码路径
	// （例如 Mobile%20Documents、中文 %E4%B8%AD%E6%96%87、C%3A/...）。若字面路径不在磁盘上则解码。
	if !hadFileScheme && strings.Contains(s, "%") {
		if unesc, err := url.PathUnescape(s); err == nil && unesc != s {
			if _, statErr := os.Stat(s); statErr != nil {
				s = unesc
			}
		}
	}
	if len(s) >= 3 && s[0] == '/' && isASCIIAlpha(s[1]) && s[2] == ':' {
		s = s[1:]
	}
	// 处理 Rust/Windows canonicalize 常见的扩展路径前缀 \\?\ 与 \\?\UNC\
	if len(s) >= 8 && (strings.EqualFold(s[:8], `\\?\UNC\`) || strings.EqualFold(s[:8], `//?/UNC/`)) {
		s = `\\` + s[8:]
	} else if strings.HasPrefix(s, `\\?\`) || strings.HasPrefix(s, `//?/`) {
		s = s[4:]
	}
	return strings.TrimSpace(s)
}

func normalizePathMatchKey(p string, isWindows bool) string {
	s := stripPathDecorators(p)
	if s == "" {
		return ""
	}
	win := isWindows || looksLikeWindowsPath(s)
	if win {
		isUNC := strings.HasPrefix(s, `\\`) || strings.HasPrefix(s, `//`)
		slashed := strings.ReplaceAll(s, `\`, `/`)
		if isUNC {
			trimmed := strings.TrimLeft(slashed, "/")
			cleaned := path.Clean("/" + trimmed)
			cleaned = strings.TrimSuffix(cleaned, "/")
			return strings.ToLower("/" + cleaned)
		}
		if len(slashed) >= 2 && isASCIIAlpha(slashed[0]) && slashed[1] == ':' {
			drive := strings.ToLower(slashed[:2])
			rest := slashed[2:]
			if rest == "" || rest == "/" {
				return drive + "/"
			}
			if !strings.HasPrefix(rest, "/") {
				rest = "/" + rest
			}
			cleaned := path.Clean(rest)
			if cleaned == "/" {
				return drive + "/"
			}
			return drive + strings.ToLower(strings.TrimSuffix(cleaned, "/"))
		}
		cleaned := path.Clean(slashed)
		if cleaned != "/" {
			cleaned = strings.TrimSuffix(cleaned, "/")
		}
		return strings.ToLower(cleaned)
	}
	cleaned := path.Clean(s)
	if cleaned != "/" {
		cleaned = strings.TrimSuffix(cleaned, "/")
	}
	return cleaned
}

func cleanNativeCwd(p string) string {
	s := stripPathDecorators(p)
	if s == "" {
		return ""
	}
	if runtime.GOOS != "windows" && looksLikeWindowsPath(s) {
		return s
	}
	return realOrClean(s)
}

// isUUIDLike 判断字符串是否像标准 UUID（例如 Claude/Codex 的原生 sessionId）。
func isUUIDLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func parseFlexibleTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// readJSONLLine 读取一行 JSONL；若单行超过 limit 字节则安全丢弃该行剩余部分并标记 skipped=true，
// 避免单个大体积工具输出或内嵌图片导致整个会话文件扫描中断。
func readJSONLLine(br *bufio.Reader, limit int) (line []byte, skipped bool, err error) {
	for {
		frag, isPrefix, rerr := br.ReadLine()
		if !skipped {
			if len(line)+len(frag) > limit {
				line = nil
				skipped = true
			} else {
				line = append(line, frag...)
			}
		}
		if !isPrefix || rerr != nil {
			return line, skipped, rerr
		}
	}
}

func getCachedNativeFile(filePath string, size, modUnix int64) (nativeFileCacheEntry, bool) {
	nativeCacheMu.Lock()
	defer nativeCacheMu.Unlock()
	ent, ok := nativeFileCache[filePath]
	if ok && ent.size == size && ent.modUnix == modUnix {
		return ent, true
	}
	return nativeFileCacheEntry{}, false
}

func putCachedNativeFile(filePath string, ent nativeFileCacheEntry) {
	nativeCacheMu.Lock()
	defer nativeCacheMu.Unlock()
	if len(nativeFileCache) > 4000 {
		clear(nativeFileCache)
	}
	nativeFileCache[filePath] = ent
}

// ---------- 1. Claude Code 原生会话发现与历史加载 (~/.claude/projects) ----------

func claudeProjectsDir() string {
	if custom := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); custom != "" {
		return filepath.Join(custom, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

func discoverNativeClaudeSessions() []nativeSessionInfo {
	root := claudeProjectsDir()
	if root == "" {
		return nil
	}
	projDirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []nativeSessionInfo
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		dirPath := filepath.Join(root, pd.Name())
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		indexHints := loadClaudeSessionsIndex(filepath.Join(dirPath, "sessions-index.json"))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".jsonl") || strings.HasPrefix(name, "agent-") {
				continue
			}
			fi, err := e.Info()
			if err != nil || fi.Size() == 0 {
				continue
			}
			fp := filepath.Join(dirPath, name)
			info, ok := parseClaudeSessionMetaCached(fp, fi.Size(), fi.ModTime(), indexHints)
			if ok {
				out = append(out, info)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func findNativeClaudeSession(nativeID string) (nativeSessionInfo, bool) {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return nativeSessionInfo{}, false
	}
	root := claudeProjectsDir()
	if root == "" {
		return nativeSessionInfo{}, false
	}
	projDirs, err := os.ReadDir(root)
	if err != nil {
		return nativeSessionInfo{}, false
	}
	targetName := nativeID + ".jsonl"
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		dirPath := filepath.Join(root, pd.Name())
		fp := filepath.Join(dirPath, targetName)
		if fi, err := os.Stat(fp); err == nil && !fi.IsDir() && fi.Size() > 0 {
			indexHints := loadClaudeSessionsIndex(filepath.Join(dirPath, "sessions-index.json"))
			if info, ok := parseClaudeSessionMetaCached(fp, fi.Size(), fi.ModTime(), indexHints); ok {
				return info, true
			}
		}
	}
	// 兜底：若文件名与内部 sessionId 不一致，走全量缓存扫描匹配
	for _, item := range discoverNativeClaudeSessions() {
		if item.NativeID == nativeID {
			return item, true
		}
	}
	return nativeSessionInfo{}, false
}

type claudeIndexHint struct {
	projectPath string
	title       string
}

func loadClaudeSessionsIndex(indexPath string) map[string]claudeIndexHint {
	raw, err := os.ReadFile(indexPath)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var idx struct {
		Entries []struct {
			SessionID   string `json:"sessionId"`
			ProjectPath string `json:"projectPath"`
			CustomTitle string `json:"customTitle"`
			Summary     string `json:"summary"`
			FirstPrompt string `json:"firstPrompt"`
		} `json:"entries"`
	}
	if json.Unmarshal(raw, &idx) != nil || len(idx.Entries) == 0 {
		return nil
	}
	out := make(map[string]claudeIndexHint, len(idx.Entries))
	for _, e := range idx.Entries {
		if e.SessionID == "" {
			continue
		}
		title := strings.TrimSpace(e.CustomTitle)
		if title == "" {
			title = strings.TrimSpace(e.Summary)
		}
		if title == "" {
			title = strings.TrimSpace(e.FirstPrompt)
		}
		out[e.SessionID] = claudeIndexHint{
			projectPath: strings.TrimSpace(e.ProjectPath),
			title:       title,
		}
	}
	return out
}

func parseClaudeSessionMetaCached(filePath string, size int64, modTime time.Time, hints map[string]claudeIndexHint) (nativeSessionInfo, bool) {
	modUnix := modTime.UnixNano()
	if ent, ok := getCachedNativeFile(filePath, size, modUnix); ok {
		return ent.info, ent.valid
	}
	info, ok := parseClaudeSessionMeta(filePath, size, modTime, hints)
	putCachedNativeFile(filePath, nativeFileCacheEntry{
		size:    size,
		modUnix: modUnix,
		valid:   ok,
		info:    info,
	})
	return info, ok
}

type claudeJSONLRecord struct {
	Type        string          `json:"type"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	Timestamp   string          `json:"timestamp"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	CustomTitle string          `json:"customTitle"`
	Summary     string          `json:"summary"`
	Message     json.RawMessage `json:"message"`
}

func parseClaudeSessionMeta(filePath string, size int64, modTime time.Time, hints map[string]claudeIndexHint) (nativeSessionInfo, bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return nativeSessionInfo{}, false
	}
	defer f.Close()

	nativeID := strings.TrimSuffix(filepath.Base(filePath), ".jsonl")
	var cwd, model, customTitle, summary, firstUser string
	var lastTS time.Time
	hasConversation := false

	processLine := func(line []byte) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			return
		}
		var rec claudeJSONLRecord
		if json.Unmarshal(line, &rec) != nil {
			return
		}
		if rec.SessionID != "" {
			nativeID = rec.SessionID
		}
		if cwd == "" && strings.TrimSpace(rec.Cwd) != "" {
			cwd = strings.TrimSpace(rec.Cwd)
		}
		if ts := parseFlexibleTime(rec.Timestamp); ts.After(lastTS) {
			lastTS = ts
		}
		switch rec.Type {
		case "custom-title":
			if t := strings.TrimSpace(rec.CustomTitle); t != "" {
				customTitle = t
			}
		case "summary":
			if t := strings.TrimSpace(rec.Summary); t != "" {
				summary = t
			}
		case "user":
			if rec.IsMeta || rec.IsSidechain {
				return
			}
			txt, _ := extractClaudeUserContent(rec.Message)
			txt = cleanClaudeUserText(txt)
			if txt != "" {
				hasConversation = true
				if firstUser == "" {
					firstUser = txt
				}
			}
		case "assistant":
			if rec.IsSidechain {
				return
			}
			hasConversation = true
			if model == "" {
				var m struct {
					Model string `json:"model"`
				}
				if json.Unmarshal(rec.Message, &m) == nil {
					if mv := strings.TrimSpace(m.Model); mv != "" && !strings.HasPrefix(mv, "<") && !strings.EqualFold(mv, "synthetic") {
						model = mv
					}
				}
			}
		}
	}

	// 1) 读文件头提取 cwd / sessionId / 首条用户消息 / model
	// 若首行包含超大 file-history-snapshot，必须继续向后扫描直到拿到 cwd 与真实对话记录（受最大行数保护）。
	br := bufio.NewReaderSize(f, 32*1024)
	bytesRead := 0
	reachedEOF := false
	for lines := 0; lines < nativeMetaMaxLines && (bytesRead < nativeMetaHeadLimit || cwd == "" || !hasConversation); lines++ {
		line, skipped, rerr := readJSONLLine(br, nativeLineScanLimit)
		bytesRead += len(line) + 1
		if !skipped && len(line) > 0 {
			processLine(line)
		}
		if rerr != nil {
			if rerr == io.EOF {
				reachedEOF = true
			}
			break
		}
		if cwd != "" && hasConversation && firstUser != "" && model != "" && bytesRead >= 256*1024 {
			break
		}
	}

	// 2) 若 head 未读到文件末尾，额外读文件尾获取最新的 custom-title / summary / timestamp
	if !reachedEOF && size > 0 {
		seekOff := size - nativeMetaTailLimit
		if seekOff < 0 {
			seekOff = 0
		}
		if _, serr := f.Seek(seekOff, io.SeekStart); serr == nil {
			tbr := bufio.NewReaderSize(f, 32*1024)
			if seekOff > 0 {
				// 丢弃可能截断的第一行
				_, _, _ = readJSONLLine(tbr, nativeLineScanLimit)
			}
			for {
				line, skipped, rerr := readJSONLLine(tbr, nativeLineScanLimit)
				if !skipped && len(line) > 0 {
					processLine(line)
				}
				if rerr != nil {
					break
				}
			}
		}
	}

	if hint, ok := hints[nativeID]; ok {
		if cwd == "" && hint.projectPath != "" {
			cwd = hint.projectPath
		}
		if customTitle == "" && summary == "" && firstUser == "" && hint.title != "" {
			summary = hint.title
		}
	}

	if !hasConversation || nativeID == "" || cwd == "" {
		return nativeSessionInfo{}, false
	}

	title := customTitle
	if title == "" {
		title = summary
	}
	if title == "" {
		title = firstUser
	}
	title = truncateRunes(firstNonEmptyLine(strings.TrimSpace(title)), 28)
	if title == "" {
		title = "新对话"
	}

	updated := modTime
	if lastTS.After(updated) {
		updated = lastTS
	}
	return nativeSessionInfo{
		ID:         "claude_" + nativeID,
		Agent:      "claude",
		NativeID:   nativeID,
		Cwd:        cleanNativeCwd(cwd),
		RawCwd:     cwd,
		Title:      title,
		Model:      model,
		UpdatedAt:  updated,
		SourcePath: filePath,
	}, true
}

type claudeToolResult struct {
	toolUseID string
	isError   bool
	output    string
}

func extractClaudeUserContent(raw json.RawMessage) (text string, results []claudeToolResult) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil || len(msg.Content) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(msg.Content, &s) == nil {
		return strings.TrimSpace(s), nil
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return "", nil
	}
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(t)
			}
		case "tool_result":
			if b.ToolUseID != "" {
				results = append(results, claudeToolResult{
					toolUseID: b.ToolUseID,
					isError:   b.IsError,
					output:    extractToolOutput(b.Content, nil),
				})
			}
		}
	}
	return strings.TrimSpace(sb.String()), results
}

func cleanClaudeUserText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// 跳过 Claude Code CLI 自动写入的本地命令提示/空系统包裹
	for _, prefix := range []string{
		"<local-command-caveat>",
		"<local-command-stdout>",
		"<local-command-stderr>",
	} {
		if strings.HasPrefix(s, prefix) {
			return ""
		}
	}
	// 若是斜杠命令包裹 <command-name>/xxx</command-name>，提取命令名用于可读展示
	if strings.HasPrefix(s, "<command-name>") {
		if end := strings.Index(s, "</command-name>"); end > len("<command-name>") {
			cmdName := strings.TrimSpace(s[len("<command-name>"):end])
			if aStart := strings.Index(s, "<command-args>"); aStart >= 0 {
				aStart += len("<command-args>")
				if aEnd := strings.Index(s[aStart:], "</command-args>"); aEnd >= 0 {
					args := strings.TrimSpace(s[aStart : aStart+aEnd])
					if args != "" {
						return cmdName + " " + args
					}
				}
			}
			return cmdName
		}
	}
	return s
}

func loadNativeClaudeHistory(filePath string) []ChatMessage {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	var msgs []ChatMessage
	toolIndex := map[string][2]int{} // toolUseID -> [msgIdx, toolIdx]

	for {
		line, skipped, rerr := readJSONLLine(br, nativeFullLineLimit)
		if !skipped && len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 && line[0] == '{' {
				var rec claudeJSONLRecord
				if json.Unmarshal(line, &rec) == nil && !rec.IsSidechain {
					ts := parseFlexibleTime(rec.Timestamp)
					if ts.IsZero() {
						ts = time.Now()
					}
					switch rec.Type {
					case "user":
						if rec.IsMeta {
							break
						}
						txt, results := extractClaudeUserContent(rec.Message)
						for _, tr := range results {
							if loc, ok := toolIndex[tr.toolUseID]; ok && loc[0] < len(msgs) && loc[1] < len(msgs[loc[0]].Tools) {
								st := "completed"
								if tr.isError {
									st = "failed"
								}
								msgs[loc[0]].Tools[loc[1]].Status = st
								if tr.output != "" {
									msgs[loc[0]].Tools[loc[1]].Output = tr.output
								}
							}
						}
						txt = cleanClaudeUserText(txt)
						if txt != "" {
							msgs = append(msgs, ChatMessage{
								ID:        newShortID("umsg"),
								Role:      "user",
								Text:      txt,
								CreatedAt: ts,
							})
						}

					case "assistant":
						var am struct {
							Content []struct {
								Type     string          `json:"type"`
								Text     string          `json:"text"`
								Thinking string          `json:"thinking"`
								ID       string          `json:"id"`
								Name     string          `json:"name"`
								Input    json.RawMessage `json:"input"`
							} `json:"content"`
						}
						if json.Unmarshal(rec.Message, &am) != nil {
							break
						}
						var textPart, thinkPart strings.Builder
						var tools []ToolCall
						for _, c := range am.Content {
							switch c.Type {
							case "thinking":
								if t := strings.TrimSpace(c.Thinking); t != "" {
									if thinkPart.Len() > 0 {
										thinkPart.WriteString("\n")
									}
									thinkPart.WriteString(t)
								}
							case "text":
								if t := strings.TrimSpace(c.Text); t != "" {
									if textPart.Len() > 0 {
										textPart.WriteString("\n\n")
									}
									textPart.WriteString(t)
								}
							case "tool_use":
								detail := extractToolDetail(c.Input, nil, nil)
								title := c.Name
								if detail != "" {
									title = truncateRunes(firstNonEmptyLine(detail), 72)
								}
								tools = append(tools, ToolCall{
									ID:     c.ID,
									Name:   c.Name,
									Title:  title,
									Status: "completed",
									Detail: detail,
								})
							}
						}
						if textPart.Len() == 0 && thinkPart.Len() == 0 && len(tools) == 0 {
							break
						}
						// 连续的 assistant 记录（思考 + 工具调用 + 最终回复）合并为一条完整回答
						if len(msgs) > 0 && msgs[len(msgs)-1].Role == "assistant" {
							last := &msgs[len(msgs)-1]
							if textPart.Len() > 0 {
								if last.Text != "" {
									last.Text += "\n\n" + textPart.String()
								} else {
									last.Text = textPart.String()
								}
							}
							if thinkPart.Len() > 0 {
								if last.Thinking != "" {
									last.Thinking = truncateStr(last.Thinking+"\n"+thinkPart.String(), 24000)
								} else {
									last.Thinking = truncateStr(thinkPart.String(), 24000)
								}
							}
							for _, tc := range tools {
								idx := len(last.Tools)
								last.Tools = append(last.Tools, tc)
								if tc.ID != "" {
									toolIndex[tc.ID] = [2]int{len(msgs) - 1, idx}
								}
							}
						} else {
							msgIdx := len(msgs)
							msgs = append(msgs, ChatMessage{
								ID:        newShortID("amsg"),
								Role:      "assistant",
								Text:      textPart.String(),
								Thinking:  truncateStr(thinkPart.String(), 24000),
								Tools:     tools,
								CreatedAt: ts,
							})
							for ti, tc := range tools {
								if tc.ID != "" {
									toolIndex[tc.ID] = [2]int{msgIdx, ti}
								}
							}
						}
					}
				}
			}
		}
		if rerr != nil {
			break
		}
	}

	if len(msgs) > maxHistoryMessages {
		msgs = msgs[len(msgs)-maxHistoryMessages:]
	}
	return msgs
}

// ---------- 2. OpenAI Codex 原生会话发现与历史加载 (~/.codex/sessions) ----------

func codexHomeDir() string {
	if custom := strings.TrimSpace(os.Getenv("CODEX_HOME")); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

func loadCodexTitles(codexHome string) map[string]string {
	out := map[string]string{}
	mergeFile := func(p string, parse func([]byte) map[string]string) {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			return
		}
		modUnix := fi.ModTime().UnixNano()
		nativeCacheMu.Lock()
		ent, ok := codexTitleCache[p]
		nativeCacheMu.Unlock()
		if ok && ent.size == fi.Size() && ent.modUnix == modUnix {
			for k, v := range ent.titles {
				if v != "" {
					out[k] = v
				}
			}
			return
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return
		}
		parsed := parse(raw)
		nativeCacheMu.Lock()
		codexTitleCache[p] = codexTitleCacheEntry{size: fi.Size(), modUnix: modUnix, titles: parsed}
		nativeCacheMu.Unlock()
		for k, v := range parsed {
			if v != "" {
				out[k] = v
			}
		}
	}

	// 1) ~/.codex/session_index.jsonl
	mergeFile(filepath.Join(codexHome, "session_index.jsonl"), func(raw []byte) map[string]string {
		m := map[string]string{}
		for _, line := range bytes.Split(raw, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var rec struct {
				ID         string `json:"id"`
				ThreadName string `json:"thread_name"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.ID != "" && strings.TrimSpace(rec.ThreadName) != "" {
				m[rec.ID] = strings.TrimSpace(rec.ThreadName)
			}
		}
		return m
	})

	// 2) ~/.codex/.codex-global-state.json (thread-titles.titles)
	mergeFile(filepath.Join(codexHome, ".codex-global-state.json"), func(raw []byte) map[string]string {
		var gs struct {
			ThreadTitles struct {
				Titles map[string]string `json:"titles"`
			} `json:"thread-titles"`
		}
		if json.Unmarshal(raw, &gs) != nil {
			return nil
		}
		return gs.ThreadTitles.Titles
	})
	return out
}

type diskFileMeta struct {
	path    string
	size    int64
	modTime time.Time
}

func collectCodexRolloutFiles(root string) []diskFileMeta {
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	var files []diskFileMeta
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if fi, ierr := d.Info(); ierr == nil && fi.Size() > 0 {
			files = append(files, diskFileMeta{path: p, size: fi.Size(), modTime: fi.ModTime()})
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	return files
}

func discoverNativeCodexSessions() []nativeSessionInfo {
	home := codexHomeDir()
	if home == "" {
		return nil
	}
	files := collectCodexRolloutFiles(filepath.Join(home, "sessions"))
	if len(files) == 0 {
		return nil
	}
	if len(files) > maxCodexScanFiles {
		files = files[:maxCodexScanFiles]
	}
	titles := loadCodexTitles(home)
	var out []nativeSessionInfo
	for _, fm := range files {
		info, ok := parseCodexSessionMetaCached(fm.path, fm.size, fm.modTime)
		if !ok {
			continue
		}
		if t := strings.TrimSpace(titles[info.NativeID]); t != "" {
			info.Title = truncateRunes(firstNonEmptyLine(t), 28)
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func findNativeCodexSession(nativeID string) (nativeSessionInfo, bool) {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return nativeSessionInfo{}, false
	}
	home := codexHomeDir()
	if home == "" {
		return nativeSessionInfo{}, false
	}
	titles := loadCodexTitles(home)
	for _, sub := range []string{"sessions", "archived_sessions"} {
		files := collectCodexRolloutFiles(filepath.Join(home, sub))
		// 优先匹配文件名包含 nativeID 的 rollout 文件（几乎总是直接命中）
		for _, fm := range files {
			if strings.Contains(filepath.Base(fm.path), nativeID) {
				if info, ok := parseCodexSessionMetaCached(fm.path, fm.size, fm.modTime); ok && info.NativeID == nativeID {
					if t := strings.TrimSpace(titles[info.NativeID]); t != "" {
						info.Title = truncateRunes(firstNonEmptyLine(t), 28)
					}
					return info, true
				}
			}
		}
		// 兜底扫内容
		for _, fm := range files {
			if info, ok := parseCodexSessionMetaCached(fm.path, fm.size, fm.modTime); ok && info.NativeID == nativeID {
				if t := strings.TrimSpace(titles[info.NativeID]); t != "" {
					info.Title = truncateRunes(firstNonEmptyLine(t), 28)
				}
				return info, true
			}
		}
	}
	return nativeSessionInfo{}, false
}

func parseCodexSessionMetaCached(filePath string, size int64, modTime time.Time) (nativeSessionInfo, bool) {
	modUnix := modTime.UnixNano()
	if ent, ok := getCachedNativeFile(filePath, size, modUnix); ok {
		return ent.info, ent.valid
	}
	info, ok := parseCodexSessionMeta(filePath, modTime)
	putCachedNativeFile(filePath, nativeFileCacheEntry{
		size:    size,
		modUnix: modUnix,
		valid:   ok,
		info:    info,
	})
	return info, ok
}

type codexRolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func parseCodexSessionMeta(filePath string, modTime time.Time) (nativeSessionInfo, bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return nativeSessionInfo{}, false
	}
	defer f.Close()

	var nativeID, cwd, model, firstUser string
	isSubagent := false
	hasConversation := false

	br := bufio.NewReaderSize(f, 32*1024)
	bytesRead := 0
	for lines := 0; lines < nativeMetaMaxLines && (bytesRead < nativeMetaHeadLimit || nativeID == "" || cwd == "" || !hasConversation); lines++ {
		line, skipped, rerr := readJSONLLine(br, nativeLineScanLimit)
		bytesRead += len(line) + 1
		if !skipped && len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 && line[0] == '{' {
				var rec codexRolloutLine
				if json.Unmarshal(line, &rec) == nil {
					switch rec.Type {
					case "session_meta":
						var sm struct {
							ID             string          `json:"id"`
							Cwd            string          `json:"cwd"`
							ParentThreadID string          `json:"parent_thread_id"`
							Source         json.RawMessage `json:"source"`
						}
						if json.Unmarshal(rec.Payload, &sm) == nil {
							if sm.ID != "" {
								nativeID = sm.ID
							}
							if sm.Cwd != "" {
								cwd = sm.Cwd
							}
							if sm.ParentThreadID != "" {
								isSubagent = true
							}
							srcTrim := bytes.TrimSpace(sm.Source)
							if len(srcTrim) > 0 && srcTrim[0] == '{' {
								// 例如 {"subagent": ...}，跳过后台子代理线程
								isSubagent = true
							}
						}
					case "turn_context":
						var tc struct {
							Cwd   string `json:"cwd"`
							Model string `json:"model"`
						}
						if json.Unmarshal(rec.Payload, &tc) == nil {
							if cwd == "" && tc.Cwd != "" {
								cwd = tc.Cwd
							}
							if model == "" && tc.Model != "" {
								model = tc.Model
							}
						}
					case "response_item":
						role, txt := extractCodexResponseMessage(rec.Payload)
						if role == "user" {
							if clean := cleanCodexUserText(txt); clean != "" {
								hasConversation = true
								if firstUser == "" {
									firstUser = clean
								}
							}
						} else if role == "assistant" && strings.TrimSpace(txt) != "" {
							hasConversation = true
						}
					case "event_msg":
						var ev struct {
							Type    string `json:"type"`
							Message string `json:"message"`
						}
						if json.Unmarshal(rec.Payload, &ev) == nil {
							switch ev.Type {
							case "user_message":
								if clean := cleanCodexUserText(ev.Message); clean != "" {
									hasConversation = true
									if firstUser == "" {
										firstUser = clean
									}
								}
							case "agent_message":
								if strings.TrimSpace(ev.Message) != "" {
									hasConversation = true
								}
							}
						}
					}
				}
			}
		}
		if isSubagent {
			return nativeSessionInfo{}, false
		}
		if nativeID != "" && cwd != "" && firstUser != "" && model != "" {
			break
		}
		if rerr != nil {
			break
		}
	}

	if isSubagent || !hasConversation || nativeID == "" || cwd == "" {
		return nativeSessionInfo{}, false
	}

	title := truncateRunes(firstNonEmptyLine(firstUser), 28)
	if title == "" {
		title = "新对话"
	}
	return nativeSessionInfo{
		ID:         "codex_" + nativeID,
		Agent:      "codex",
		NativeID:   nativeID,
		Cwd:        cleanNativeCwd(cwd),
		RawCwd:     cwd,
		Title:      title,
		Model:      model,
		UpdatedAt:  modTime,
		SourcePath: filePath,
	}, true
}

func extractCodexResponseMessage(payload json.RawMessage) (role, text string) {
	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(payload, &item) != nil || item.Type != "message" {
		return "", ""
	}
	var sb strings.Builder
	for _, c := range item.Content {
		switch c.Type {
		case "input_text", "output_text", "text":
			if t := strings.TrimSpace(c.Text); t != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(t)
			}
		}
	}
	return item.Role, strings.TrimSpace(sb.String())
}

// cleanCodexUserText 过滤 Codex 自动注入的系统/环境指令块，并提取 VS Code 扩展包装后的真实用户提问。
func cleanCodexUserText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, prefix := range []string{
		"# AGENTS.md instructions",
		"<environment_context>",
		"<INSTRUCTIONS>",
		"<permissions instructions>",
		"<collaboration_mode>",
		"<user_instructions>",
		"<app-context>",
		"<turn_aborted>",
		"<subagent_notification>",
	} {
		if strings.HasPrefix(s, prefix) {
			return ""
		}
	}
	// VS Code Codex 扩展会把用户请求包在 "## My request for Codex:\n..." 之后
	if idx := strings.Index(s, "## My request for Codex:"); idx >= 0 {
		after := strings.TrimSpace(s[idx+len("## My request for Codex:"):])
		if after != "" {
			return after
		}
	}
	return s
}

func loadNativeCodexHistory(filePath string) []ChatMessage {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	var msgs []ChatMessage
	var fallbackMsgs []ChatMessage
	toolIndex := map[string][2]int{}

	ensureAssistant := func(ts time.Time) *ChatMessage {
		if len(msgs) > 0 && msgs[len(msgs)-1].Role == "assistant" {
			return &msgs[len(msgs)-1]
		}
		msgs = append(msgs, ChatMessage{
			ID:        newShortID("amsg"),
			Role:      "assistant",
			CreatedAt: ts,
		})
		return &msgs[len(msgs)-1]
	}

	for {
		line, skipped, rerr := readJSONLLine(br, nativeFullLineLimit)
		if !skipped && len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 && line[0] == '{' {
				var rec codexRolloutLine
				if json.Unmarshal(line, &rec) == nil {
					ts := parseFlexibleTime(rec.Timestamp)
					if ts.IsZero() {
						ts = time.Now()
					}
					switch rec.Type {
					case "response_item":
						var head struct {
							Type string `json:"type"`
						}
						if json.Unmarshal(rec.Payload, &head) != nil {
							break
						}
						switch head.Type {
						case "message":
							role, txt := extractCodexResponseMessage(rec.Payload)
							if role == "user" {
								if clean := cleanCodexUserText(txt); clean != "" {
									msgs = append(msgs, ChatMessage{
										ID:        newShortID("umsg"),
										Role:      "user",
										Text:      clean,
										CreatedAt: ts,
									})
								}
							} else if role == "assistant" && txt != "" {
								asst := ensureAssistant(ts)
								if asst.Text != "" {
									asst.Text += "\n\n" + txt
								} else {
									asst.Text = txt
								}
							}

						case "reasoning":
							var rs struct {
								Summary []struct {
									Text string `json:"text"`
								} `json:"summary"`
							}
							if json.Unmarshal(rec.Payload, &rs) == nil && len(rs.Summary) > 0 {
								var sb strings.Builder
								for _, item := range rs.Summary {
									if t := strings.TrimSpace(item.Text); t != "" {
										if sb.Len() > 0 {
											sb.WriteString("\n")
										}
										sb.WriteString(t)
									}
								}
								if sb.Len() > 0 {
									asst := ensureAssistant(ts)
									if asst.Thinking != "" {
										asst.Thinking = truncateStr(asst.Thinking+"\n"+sb.String(), 24000)
									} else {
										asst.Thinking = truncateStr(sb.String(), 24000)
									}
								}
							}

						case "function_call", "custom_tool_call":
							var fc struct {
								CallID    string `json:"call_id"`
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
								Input     string `json:"input"`
							}
							if json.Unmarshal(rec.Payload, &fc) == nil && fc.Name != "" {
								rawArg := strings.TrimSpace(fc.Arguments)
								if rawArg == "" {
									rawArg = strings.TrimSpace(fc.Input)
								}
								detail := extractToolDetail(json.RawMessage(rawArg), nil, nil)
								if detail == "" && rawArg != "" {
									detail = truncateRunes(rawArg, 500)
								}
								title := fc.Name
								if detail != "" {
									title = truncateRunes(firstNonEmptyLine(detail), 72)
								}
								asst := ensureAssistant(ts)
								idx := len(asst.Tools)
								asst.Tools = append(asst.Tools, ToolCall{
									ID:     fc.CallID,
									Name:   fc.Name,
									Title:  title,
									Status: "completed",
									Detail: detail,
								})
								if fc.CallID != "" {
									toolIndex[fc.CallID] = [2]int{len(msgs) - 1, idx}
								}
							}

						case "function_call_output", "custom_tool_call_output":
							var out struct {
								CallID string `json:"call_id"`
								Output string `json:"output"`
							}
							if json.Unmarshal(rec.Payload, &out) == nil && out.CallID != "" {
								if loc, ok := toolIndex[out.CallID]; ok && loc[0] < len(msgs) && loc[1] < len(msgs[loc[0]].Tools) {
									if txt := strings.TrimSpace(out.Output); txt != "" {
										msgs[loc[0]].Tools[loc[1]].Output = truncateStr(txt, 4000)
									}
								}
							}
						}

					case "event_msg":
						var ev struct {
							Type    string `json:"type"`
							Message string `json:"message"`
						}
						if json.Unmarshal(rec.Payload, &ev) == nil {
							switch ev.Type {
							case "user_message":
								if clean := cleanCodexUserText(ev.Message); clean != "" {
									fallbackMsgs = append(fallbackMsgs, ChatMessage{
										ID:        newShortID("umsg"),
										Role:      "user",
										Text:      clean,
										CreatedAt: ts,
									})
								}
							case "agent_message":
								if txt := strings.TrimSpace(ev.Message); txt != "" {
									fallbackMsgs = append(fallbackMsgs, ChatMessage{
										ID:        newShortID("amsg"),
										Role:      "assistant",
										Text:      txt,
										CreatedAt: ts,
									})
								}
							}
						}
					}
				}
			}
		}
		if rerr != nil {
			break
		}
	}

	if len(msgs) == 0 && len(fallbackMsgs) > 0 {
		msgs = fallbackMsgs
	}
	if len(msgs) > maxHistoryMessages {
		msgs = msgs[len(msgs)-maxHistoryMessages:]
	}
	return msgs
}
