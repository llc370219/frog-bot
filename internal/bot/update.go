package bot

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"frogbot/internal/rootd"
)

const (
	releasesURL      = "https://github.com/llc370219/frog-bot/releases"
	latestReleaseAPI = "https://api.github.com/repos/llc370219/frog-bot/releases/latest"
	listReleasesAPI  = "https://api.github.com/repos/llc370219/frog-bot/releases?per_page=30"
)

// Version 由构建脚本通过 -ldflags "-X frogbot/internal/bot.Version=vX.Y.Z" 注入；main 也会同步。
var Version string

func currentVersion() string {
	if Version != "" {
		return Version
	}
	if build, ok := debug.ReadBuildInfo(); ok && build.Main.Version != "" && build.Main.Version != "(devel)" {
		return build.Main.Version
	}
	return "开发版"
}

type updateChannelPref struct {
	Channel string `json:"channel"`
}

func normalizeChannel(ch, cur string) string {
	switch strings.ToLower(strings.TrimSpace(ch)) {
	case "beta":
		return "beta"
	case "stable":
		return "stable"
	}
	if strings.Contains(cur, "-") {
		return "beta"
	}
	return "stable"
}

func (b *Bot) savedUpdateChannel() string {
	raw, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "update.json"))
	if err == nil {
		var p updateChannelPref
		if json.Unmarshal(raw, &p) == nil && (p.Channel == "stable" || p.Channel == "beta") {
			return p.Channel
		}
	}
	return normalizeChannel("", currentVersion())
}

func (b *Bot) saveUpdateChannel(ch string) string {
	ch = normalizeChannel(ch, currentVersion())
	raw, _ := json.Marshal(updateChannelPref{Channel: ch})
	_ = os.WriteFile(filepath.Join(b.cfg.DataDir, "update.json"), raw, 0o600)
	return ch
}

type releaseItem struct {
	Tag        string `json:"tag"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

type updateStatus struct {
	Current      string        `json:"current"`
	Latest       string        `json:"latest,omitempty"`
	Channel      string        `json:"channel"`
	ReleaseURL   string        `json:"release_url"`
	State        string        `json:"state"`
	CanApply     bool          `json:"can_apply"`
	NeedPassword bool          `json:"need_password,omitempty"`
	Releases     []releaseItem `json:"releases"`
}

var releaseClient = &http.Client{Timeout: 10 * time.Second}
var upgradeDownloadClient = &http.Client{Timeout: 5 * time.Minute}

func checkUpdate(ctx context.Context, client *http.Client, endpoint, current string) (updateStatus, error) {
	ch := "stable"
	if strings.Contains(endpoint, "per_page=") {
		ch = "beta"
	}
	return checkUpdateForChannel(ctx, client, endpoint, current, ch)
}

func checkUpdateForChannel(ctx context.Context, client *http.Client, endpoint, current, channel string) (updateStatus, error) {
	channel = normalizeChannel(channel, current)
	canApply, needPw := detectUpgradeCapability()
	status := updateStatus{
		Current:      current,
		Channel:      channel,
		ReleaseURL:   releasesURL,
		State:        "no_release",
		CanApply:     canApply,
		NeedPassword: needPw,
		Releases:     []releaseItem{},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return status, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return status, errors.New("无法连接 GitHub")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return status, nil
	}
	if resp.StatusCode != http.StatusOK {
		return status, fmt.Errorf("GitHub HTTP %d", resp.StatusCode)
	}

	tag, rels, err := extractReleases(resp.Body, channel)
	if err != nil {
		return status, err
	}
	status.Releases = rels
	if tag == "" {
		return status, nil
	}

	status.Latest = tag
	status.ReleaseURL = releasesURL + "/tag/" + url.PathEscape(tag)
	status.State = compareChannelState(current, tag, channel)
	return status, nil
}

func extractReleases(r io.Reader, channel string) (string, []releaseItem, error) {
	body, err := io.ReadAll(io.LimitReader(r, 2<<20))
	if err != nil {
		return "", []releaseItem{}, errors.New("读取发布信息失败")
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var rel struct {
			Tag        string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.Unmarshal(trimmed, &rel); err != nil || strings.TrimSpace(rel.Tag) == "" {
			return "", []releaseItem{}, errors.New("解析发布信息失败")
		}
		if rel.Draft {
			return "", []releaseItem{}, nil
		}
		t := strings.TrimSpace(rel.Tag)
		pre := rel.Prerelease || strings.Contains(t, "-")
		return t, []releaseItem{{Tag: t, Prerelease: pre}}, nil
	}

	var list []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(trimmed, &list); err != nil {
		return "", []releaseItem{}, errors.New("解析发布列表失败")
	}
	seen := map[string]bool{}
	items := make([]releaseItem, 0, len(list))
	for _, rel := range list {
		t := strings.TrimSpace(rel.Tag)
		if rel.Draft || !releaseVersion.MatchString(t) || !strings.HasPrefix(t, "v") || seen[t] {
			continue
		}
		isPre := rel.Prerelease || strings.Contains(t, "-")
		if channel == "stable" && isPre {
			continue
		}
		seen[t] = true
		items = append(items, releaseItem{Tag: t, Prerelease: isPre})
	}
	sort.Slice(items, func(i, j int) bool {
		newer, ok := newerSemver(items[j].Tag, items[i].Tag)
		return ok && newer
	})
	if len(items) == 0 {
		return "", items, nil
	}
	return items[0].Tag, items, nil
}

var releaseVersion = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// newerRelease 保持向后兼容（仅比较正式版与当前版本）。
func newerRelease(current, latest string) (newer, comparable bool) {
	a, b := releaseVersion.FindStringSubmatch(current), releaseVersion.FindStringSubmatch(latest)
	if a == nil || b == nil || b[4] != "" {
		return false, false
	}
	return newerSemver(current, latest)
}

// newerSemver 比较任意两个 vX.Y.Z[-prerelease] 版本，判断 target 是否比 current 更新。
func newerSemver(current, target string) (newer, comparable bool) {
	a, b := releaseVersion.FindStringSubmatch(current), releaseVersion.FindStringSubmatch(target)
	if a == nil || b == nil {
		return false, false
	}
	for i := 1; i <= 3; i++ {
		left, errA := strconv.ParseUint(a[i], 10, 64)
		right, errB := strconv.ParseUint(b[i], 10, 64)
		if errA != nil || errB != nil {
			return false, false
		}
		if left != right {
			return left < right, true
		}
	}
	preA, preB := a[4], b[4]
	if preA == preB {
		return false, true
	}
	// 同主版本号下：无后缀正式版 > 预发布版
	if preA != "" && preB == "" {
		return true, true
	}
	if preA == "" && preB != "" {
		return false, true
	}
	return comparePrerelease(strings.TrimPrefix(preA, "-"), strings.TrimPrefix(preB, "-")) < 0, true
}

func comparePrerelease(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		if i >= len(pa) {
			return -1
		}
		if i >= len(pb) {
			return 1
		}
		na, errA := strconv.ParseUint(pa[i], 10, 64)
		nb, errB := strconv.ParseUint(pb[i], 10, 64)
		if errA == nil && errB == nil {
			if na < nb {
				return -1
			}
			if na > nb {
				return 1
			}
		} else if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func compareChannelState(current, latest, channel string) string {
	newer, ok := newerSemver(current, latest)
	if !ok {
		return "unversioned"
	}
	if newer {
		return "available"
	}
	// 当前在 beta 版、主动切回 stable 通道且版本不同：允许切回最新正式版
	if channel == "stable" && strings.Contains(current, "-") && !strings.Contains(latest, "-") && current != latest {
		return "available"
	}
	return "current"
}

// ---------- 应用内一键自升级 ----------

func detectUpgradeCapability() (canApply bool, needPassword bool) {
	if canWriteSelfBinary() {
		return true, false
	}
	if p := currentPrivilege(); p != nil && p.Client != nil && p.Client.Installed() && runtime.GOOS == "linux" {
		return true, true
	}
	return false, false
}

type UpdateStatus = updateStatus

// CheckUpdateChannel 查询指定更新通道（stable / beta）的最新版本与发布列表。
func CheckUpdateChannel(ctx context.Context, current, channel string) (UpdateStatus, error) {
	ch := normalizeChannel(channel, current)
	endpoint := listReleasesAPI
	if ch == "stable" {
		endpoint = listReleasesAPI
	}
	return checkUpdateForChannel(ctx, releaseClient, endpoint, current, ch)
}

// LoadUpdateChannel 读取保存的更新通道偏好（stable / beta）。
func LoadUpdateChannel(dataDir, current string) string {
	raw, err := os.ReadFile(filepath.Join(dataDir, "update.json"))
	if err == nil {
		var p updateChannelPref
		if json.Unmarshal(raw, &p) == nil && (p.Channel == "stable" || p.Channel == "beta") {
			return p.Channel
		}
	}
	return normalizeChannel("", current)
}

// SaveUpdateChannel 保存更新通道偏好（stable / beta）。
func SaveUpdateChannel(dataDir, channel, current string) string {
	ch := normalizeChannel(channel, current)
	_ = os.MkdirAll(dataDir, 0o700)
	raw, _ := json.Marshal(updateChannelPref{Channel: ch})
	_ = os.WriteFile(filepath.Join(dataDir, "update.json"), raw, 0o600)
	return ch
}

// SelfReplaceBinary 下载指定版本发布包、校验 SHA256 并原子替换当前可执行文件（若同目录存在独立副本 frog/frogbot 也会同步更新）。
func SelfReplaceBinary(ctx context.Context, tag string) (string, error) {
	return selfReplaceBinary(ctx, tag)
}

// ValidReleaseTag 检查版本号是否符合 vX.Y.Z[-prerelease] 格式。
func ValidReleaseTag(tag string) bool {
	return validTagRe.MatchString(strings.TrimSpace(tag))
}

func canWriteSelfBinary() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if realExe, err := filepath.EvalSymlinks(exe); err == nil {
		exe = realExe
	}
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".frog-upd-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

var validTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

func (b *Bot) applyUpdate(ctx context.Context, channel, targetVersion, password string) (map[string]any, error) {
	ch := b.saveUpdateChannel(channel)
	tag := strings.TrimSpace(targetVersion)
	if tag == "" {
		st, err := checkUpdateForChannel(ctx, releaseClient, listReleasesAPI, currentVersion(), ch)
		if err != nil {
			return nil, err
		}
		tag = strings.TrimSpace(st.Latest)
	}
	if !validTagRe.MatchString(tag) {
		return nil, errors.New("未找到可安装的目标版本")
	}

	if canWriteSelfBinary() {
		exePath, err := selfReplaceBinary(ctx, tag)
		if err != nil {
			return nil, err
		}
		go func() {
			time.Sleep(350 * time.Millisecond)
			reexecSelf(exePath)
		}()
		return map[string]any{"ok": true, "version": tag, "restarting": true}, nil
	}

	p := currentPrivilege()
	if p != nil && p.Client != nil && p.Client.Installed() && runtime.GOOS == "linux" {
		authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, authErr := p.Client.Do(authCtx, rootd.Request{Op: "authorize", Password: password})
		cancel()
		if authErr != nil {
			if password == "" {
				return map[string]any{"need_password": true, "version": tag}, nil
			}
			return nil, authErr
		}
		cmd := fmt.Sprintf("nohup sh -c 'curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | VERSION=%s bash >/var/log/frogbot-upgrade.log 2>&1' >/dev/null 2>&1 &", tag)
		if _, err := p.Client.Do(ctx, rootd.Request{Op: "run", Command: cmd, TimeoutSec: 30, Task: "system-upgrade"}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "version": tag, "restarting": true}, nil
	}

	return nil, errors.New("当前运行目录无写入权限，请在终端执行安装命令升级")
}

func selfReplaceBinary(ctx context.Context, tag string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if realExe, err := filepath.EvalSymlinks(exe); err == nil {
		exe = realExe
	}
	exe = strings.TrimSuffix(exe, " (deleted)")

	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	archiveName := fmt.Sprintf("frogbot_%s_%s_%s%s", tag, runtime.GOOS, runtime.GOARCH, ext)
	baseURL := fmt.Sprintf("%s/download/%s/", releasesURL, url.PathEscape(tag))

	sumsRaw, err := httpFetchBytes(ctx, baseURL+"SHA256SUMS", 1<<20)
	if err != nil {
		return "", fmt.Errorf("下载校验文件失败: %w", err)
	}
	wantHash := ""
	for _, line := range strings.Split(string(sumsRaw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == archiveName {
			wantHash = strings.ToLower(fields[0])
			break
		}
	}
	if len(wantHash) != 64 {
		return "", fmt.Errorf("发布包尚未支持当前平台 (%s/%s)", runtime.GOOS, runtime.GOARCH)
	}

	archiveBytes, err := httpFetchBytes(ctx, baseURL+archiveName, 200<<20)
	if err != nil {
		return "", fmt.Errorf("下载安装包失败: %w", err)
	}
	sum := sha256.Sum256(archiveBytes)
	if hex.EncodeToString(sum[:]) != wantHash {
		return "", errors.New("安装包 SHA256 校验失败，已中止升级")
	}

	binBytes, err := extractBinaryFromArchive(archiveBytes, runtime.GOOS == "windows")
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(exe)
	tmpFile, err := os.CreateTemp(dir, ".frogbot-new-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(binBytes); err != nil {
		_ = tmpFile.Close()
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}

	if runtime.GOOS == "windows" {
		oldPath := exe + ".old"
		_ = os.Remove(oldPath)
		if err := os.Rename(exe, oldPath); err != nil {
			return "", err
		}
		if err := os.Rename(tmpPath, exe); err != nil {
			_ = os.Rename(oldPath, exe)
			return "", err
		}
		syncSiblingBinary(exe, binBytes)
		return exe, nil
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return "", err
	}
	syncSiblingBinary(exe, binBytes)
	return exe, nil
}

// syncSiblingBinary 同步更新同目录下独立的 frog / frogbot 副本（软链会自动指向新文件，无需覆盖）。
func syncSiblingBinary(exe string, binBytes []byte) {
	base := strings.ToLower(filepath.Base(exe))
	var siblingName string
	switch base {
	case "frogbot":
		siblingName = "frog"
	case "frog":
		siblingName = "frogbot"
	case "frogbot.exe":
		siblingName = "frog.exe"
	case "frog.exe":
		siblingName = "frogbot.exe"
	default:
		return
	}
	sib := filepath.Join(filepath.Dir(exe), siblingName)
	fi, err := os.Lstat(sib)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.IsDir() {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(sib), ".frog-sib-*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(binBytes); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Chmod(tmpPath, 0o755)
	if runtime.GOOS == "windows" {
		old := sib + ".old"
		_ = os.Remove(old)
		if os.Rename(sib, old) == nil {
			if os.Rename(tmpPath, sib) != nil {
				_ = os.Rename(old, sib)
			}
		}
		return
	}
	_ = os.Rename(tmpPath, sib)
}

func httpFetchBytes(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := upgradeDownloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func extractBinaryFromArchive(archive []byte, isZip bool) ([]byte, error) {
	wantName := "frogbot"
	if isZip {
		wantName = "frogbot.exe"
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == wantName {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 200<<20))
			}
		}
		return nil, errors.New("压缩包中未找到可执行文件")
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == wantName && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
	return nil, errors.New("压缩包中未找到可执行文件")
}
