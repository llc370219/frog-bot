package onefrog

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"frogbot/internal/sandbox"
)

// 一键安装 CLI：服务用户没有 root（systemd 设了 NoNewPrivileges，sudo 永远不可用），所以 frog 把
// Node.js 官方二进制装到自己的数据目录 <data>/tools/node，再用它的 npm 把 CLI 装到 <data>/tools/clis。
// 检测和启动 CLI 时都会查这两个目录，不需要改 PATH，也不碰系统目录。

// nodeMajor 是安装的 Node 大版本：各 CLI 的最低要求里最高的是 pi（>=22.19），22 是 LTS。
const nodeMajor = 22

// npmPackages 是能一键安装的 CLI 和它们的 npm 包名。不在这里的 CLI 需要手动安装。
var npmPackages = map[string]string{
	"codex":  "@openai/codex",
	"claude": "@anthropic-ai/claude-code",
	"pi":     "@earendil-works/pi-coding-agent",
}

// InstallState 是一次安装的进度，放在 CLIStatus 里返回给界面。
type InstallState struct {
	State      string    `json:"state"` // running / done / failed
	Step       string    `json:"step,omitempty"`
	Error      string    `json:"error,omitempty"`
	Log        string    `json:"log,omitempty"` // 最后几行输出，出错时方便排查
	Version    string    `json:"version,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type installer struct {
	mu      sync.Mutex
	dir     string // <data>/tools；为空时不支持一键安装
	jobs    map[string]*InstallState
	running string
}

var tools = &installer{jobs: map[string]*InstallState{}}

// SetToolsDir 设置一键安装用的目录（程序启动时调用一次）。
func SetToolsDir(dir string) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	tools.dir = dir
}

func toolsDir() string {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	return tools.dir
}

func managedCLIBin() string {
	if d := toolsDir(); d != "" {
		return filepath.Join(d, "clis", "bin")
	}
	return ""
}

func managedNodeBin() string {
	if d := toolsDir(); d != "" {
		return filepath.Join(d, "node", "bin")
	}
	return ""
}

// installSupported 说明这台机器能不能一键安装这个 CLI；不能时返回原因。
func installSupported(id string) error {
	if !SupportedCLI(id) {
		return fmt.Errorf("%s 暂不支持", CLIName(id))
	}
	if npmPackages[id] == "" {
		return fmt.Errorf("%s 暂不支持一键安装，请按官方说明手动安装", CLIName(id))
	}
	if _, err := nodePlatform(); err != nil {
		return err
	}
	if toolsDir() == "" {
		return errors.New("没有设置安装目录")
	}
	return nil
}

func installSnapshot(id string) *InstallState {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if j := tools.jobs[id]; j != nil {
		c := *j
		return &c
	}
	return nil
}

func (in *installer) update(id string, f func(*InstallState)) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if j := in.jobs[id]; j != nil {
		f(j)
	}
}

// StartInstall 在后台安装（或更新到最新版）一个 CLI。同一时间只装一个。
func StartInstall(id string) error {
	if CLIName(id) == "" {
		return fmt.Errorf("不支持的 Agent CLI：%s", id)
	}
	if err := installSupported(id); err != nil {
		return err
	}
	in := tools
	in.mu.Lock()
	if in.running != "" {
		name := CLIName(in.running)
		in.mu.Unlock()
		return fmt.Errorf("正在安装 %s，请等它完成", name)
	}
	in.running = id
	in.jobs[id] = &InstallState{State: "running", Step: "准备", StartedAt: time.Now()}
	dir := in.dir
	in.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		version, log, err := in.install(ctx, id, dir)
		in.mu.Lock()
		defer in.mu.Unlock()
		in.running = ""
		j := in.jobs[id]
		j.FinishedAt, j.Log, j.Step = time.Now(), tail(log, 3000), ""
		if err != nil {
			j.State, j.Error = "failed", err.Error()
			return
		}
		j.State, j.Version = "done", version
	}()
	return nil
}

func (in *installer) install(ctx context.Context, id, dir string) (version, log string, err error) {
	step := func(s string) { in.update(id, func(j *InstallState) { j.Step = s }) }
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("无法创建安装目录 %s：%v", dir, err)
	}
	node := filepath.Join(dir, "node")
	if _, err := nodeVersion(ctx, filepath.Join(node, "bin", "node")); err != nil {
		step("下载 Node.js")
		if err := installNode(ctx, dir, step); err != nil {
			return "", "", err
		}
	}
	step("用 npm 安装 " + npmPackages[id])
	prefix := filepath.Join(dir, "clis")
	cmd := exec.CommandContext(ctx, filepath.Join(node, "bin", "npm"), "install", "--global", "--prefix", prefix,
		"--no-fund", "--no-audit", "--loglevel=warn", npmPackages[id]+"@latest")
	// npm 的安装脚本也在沙箱里跑，只额外开放 tools 目录可写。
	if err := sandboxed(cmd, sandbox.Rule{Path: dir, Access: sandbox.ReadWrite}); err != nil {
		return "", "", err
	}
	cmd.Dir = dir
	cmd.Env = withPathDir(append(cmd.Environ(), "npm_config_update_notifier=false"), filepath.Join(node, "bin"))
	var out cliBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return "", out.String(), fmt.Errorf("npm 安装失败（%v），详见日志", err)
	}
	step("检查能否运行")
	exe := filepath.Join(prefix, "bin", id)
	if !isExecutable(exe) {
		return "", out.String(), fmt.Errorf("安装完成但没有找到 %s", exe)
	}
	vctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	check := exec.CommandContext(vctx, exe, "--version")
	if err := sandboxed(check); err != nil {
		return "", out.String(), err
	}
	check.Dir = dir
	check.Env = withPathDir(withPathDir(check.Environ(), filepath.Join(node, "bin")), filepath.Dir(exe))
	v, err := check.CombinedOutput()
	if err != nil {
		return "", out.String() + string(v), fmt.Errorf("%s 装好了但运行 --version 失败：%s", CLIName(id), Truncate(strings.TrimSpace(string(v)), 400))
	}
	return firstLine(string(v)), out.String(), nil
}

// nodeVersion 运行 node -v，确认能跑并且大版本够新。
func nodeVersion(ctx context.Context, node string) (string, error) {
	if !isExecutable(node) {
		return "", os.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "-v").CombinedOutput()
	v := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("Node.js 无法运行：%s", Truncate(v, 400))
	}
	var major int
	if _, err := fmt.Sscanf(v, "v%d.", &major); err != nil || major < nodeMajor {
		return "", fmt.Errorf("Node.js 版本 %s 太旧", v)
	}
	return v, nil
}

func nodePlatform() (string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("一键安装不支持 %s 架构，请手动安装", runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "linux", "darwin":
		return runtime.GOOS + "-" + arch, nil
	}
	return "", fmt.Errorf("一键安装暂不支持 %s，请手动安装 CLI", runtime.GOOS)
}

// nodeMirror 是 Node.js 下载地址；国内网络可以设 FROGBOT_NODE_MIRROR=https://npmmirror.com/mirrors/node。
func nodeMirror() string {
	if m := strings.TrimRight(strings.TrimSpace(os.Getenv("FROGBOT_NODE_MIRROR")), "/"); strings.HasPrefix(m, "https://") {
		return m
	}
	return "https://nodejs.org/dist"
}

var downloadClient = &http.Client{Timeout: 15 * time.Minute}

func fetch(ctx context.Context, url string, limit int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载 %s 失败：%v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("下载 %s 失败：HTTP %d", url, resp.StatusCode)
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(resp.Body, limit), resp.Body}, nil
}

// latestNode 从官方版本列表里找最新的 22.x。
func latestNode(ctx context.Context, mirror, platform string) (string, error) {
	body, err := fetch(ctx, mirror+"/index.json", 8<<20)
	if err != nil {
		return "", err
	}
	defer body.Close()
	var list []struct {
		Version string   `json:"version"`
		Files   []string `json:"files"`
	}
	if err := json.NewDecoder(body).Decode(&list); err != nil {
		return "", errors.New("读不懂 Node.js 版本列表")
	}
	want := strings.Replace(platform, "darwin", "osx", 1) // index.json 里 macOS 写作 osx-arm64-tar
	for _, r := range list {
		if !strings.HasPrefix(r.Version, fmt.Sprintf("v%d.", nodeMajor)) {
			continue
		}
		for _, f := range r.Files {
			if strings.HasPrefix(f, want) {
				return r.Version, nil
			}
		}
	}
	return "", fmt.Errorf("没有找到适合 %s 的 Node.js %d", platform, nodeMajor)
}

// installNode 下载 Node.js 官方压缩包，按 SHASUMS256.txt 校验，解压到 <dir>/node。
func installNode(ctx context.Context, dir string, step func(string)) error {
	platform, err := nodePlatform()
	if err != nil {
		return err
	}
	mirror := nodeMirror()
	version, err := latestNode(ctx, mirror, platform)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("node-%s-%s.tar.gz", version, platform)
	sums, err := fetch(ctx, fmt.Sprintf("%s/%s/SHASUMS256.txt", mirror, version), 1<<20)
	if err != nil {
		return err
	}
	var want string
	sc := bufio.NewScanner(sums)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	sums.Close()
	if want == "" {
		return fmt.Errorf("校验文件里没有 %s", name)
	}

	step("下载 Node.js " + version)
	body, err := fetch(ctx, fmt.Sprintf("%s/%s/%s", mirror, version, name), 512<<20)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".node-*.tar.gz")
	if err != nil {
		body.Close()
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), body)
	body.Close()
	if err != nil {
		tmp.Close()
		return fmt.Errorf("下载 Node.js 中断：%v", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		tmp.Close()
		return errors.New("Node.js 压缩包校验失败（SHA256 不一致），已丢弃")
	}

	step("解压 Node.js " + version)
	staging, err := os.MkdirTemp(dir, ".node-new-")
	if err != nil {
		tmp.Close()
		return err
	}
	defer os.RemoveAll(staging)
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return err
	}
	err = extractTarGz(tmp, staging)
	tmp.Close()
	if err != nil {
		return fmt.Errorf("解压 Node.js 失败：%v", err)
	}
	if _, err := nodeVersion(ctx, filepath.Join(staging, "bin", "node")); err != nil {
		return fmt.Errorf("%v。这台机器可能缺少系统库，请让管理员安装后重试", err)
	}
	target := filepath.Join(dir, "node")
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return os.Rename(staging, target)
}

// extractTarGz 解压 Node.js 的压缩包：去掉第一层目录（node-vX-os-arch/），拒绝跳出目标目录的路径和链接。
func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	inside := func(p string) bool {
		rel, err := filepath.Rel(dest, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		_, rest, ok := strings.Cut(strings.TrimPrefix(hdr.Name, "./"), "/")
		if !ok || rest == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rest))
		if !inside(target) {
			return fmt.Errorf("压缩包里有不安全的路径：%s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o755|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(hdr.Linkname) || !inside(filepath.Join(filepath.Dir(target), hdr.Linkname)) {
				return fmt.Errorf("压缩包里有不安全的链接：%s -> %s", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return Truncate(s, 120)
}
