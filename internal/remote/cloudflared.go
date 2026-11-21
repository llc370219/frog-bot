package remote

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"
)

var tryCloudflarePattern = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

type quickTunnel struct {
	localLn net.Listener
	localSv *http.Server
	cancel  context.CancelFunc
	done    chan struct{}

	mu      sync.RWMutex
	baseURL string
	status  string
	lastErr string
}

func startQuickTunnel(dataDir string, gateway http.Handler) (*quickTunnel, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &quickTunnel{
		localLn: ln,
		localSv: &http.Server{
			Handler:           gateway,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       95 * time.Second,
		},
		cancel: cancel,
		done:   make(chan struct{}),
		status: "正在准备加密云穿透组件…",
	}
	go func() { _ = q.localSv.Serve(ln) }()
	go q.supervise(ctx, dataDir, ln.Addr().String())
	return q, nil
}

func (q *quickTunnel) State() (baseURL, status, lastErr string) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.baseURL, q.status, q.lastErr
}

func (q *quickTunnel) setState(baseURL, status, lastErr string) {
	q.mu.Lock()
	q.baseURL = baseURL
	q.status = status
	q.lastErr = lastErr
	q.mu.Unlock()
}

func (q *quickTunnel) Close() {
	q.cancel()
	_ = q.localLn.Close()
	_ = q.localSv.Close()
	<-q.done
}

func (q *quickTunnel) supervise(ctx context.Context, dataDir, targetAddr string) {
	defer close(q.done)
	exe, err := ensureCloudflaredBinary(ctx, dataDir, func(msg string) {
		q.setState("", msg, "")
	})
	if err != nil {
		if ctx.Err() == nil {
			q.setState("", "", err.Error())
		}
		return
	}

	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		q.setState("", "正在建立免配置加密隧道…", "")
		err := q.runOnce(ctx, exe, targetAddr)
		if ctx.Err() != nil {
			return
		}
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		q.setState("", "隧道已断开，正在重连…", errMsg)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < 20*time.Second {
			backoff *= 2
		}
	}
}

func (q *quickTunnel) runOnce(ctx context.Context, exe, targetAddr string) error {
	cmd := exec.CommandContext(ctx, exe, "tunnel", "--no-autoupdate", "--url", "http://"+targetAddr)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法启动 cloudflared: %w", err)
	}

	scan := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if m := tryCloudflarePattern.FindString(line); m != "" {
				q.setState(m, "加密云穿透已就绪", "")
			}
		}
	}
	go scan(stderr)
	go scan(stdout)
	return cmd.Wait()
}

func ensureCloudflaredBinary(ctx context.Context, dataDir string, progress func(string)) (string, error) {
	if p, err := exec.LookPath("cloudflared"); err == nil && p != "" {
		return p, nil
	}
	binName := "cloudflared"
	if runtime.GOOS == "windows" {
		binName = "cloudflared.exe"
	}
	localBin := filepath.Join(dataDir, "bin", binName)
	if st, err := os.Stat(localBin); err == nil && !st.IsDir() && st.Size() > 1<<20 {
		return localBin, nil
	}

	dlURL, isTGZ, err := cloudflaredDownloadURL()
	if err != nil {
		return "", err
	}
	progress("首次启用：正在自动下载官方加密隧道组件（约 20MB，仅需一次）…")

	if err := os.MkdirAll(filepath.Dir(localBin), 0o700); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("自动下载加密隧道组件失败（请检查网络，或改用自建中转/直连加密模式）: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载加密隧道组件返回 HTTP %d", resp.StatusCode)
	}

	tmpPath := localBin + ".tmp"
	defer os.Remove(tmpPath)

	if isTGZ {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		extracted := false
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", err
			}
			if filepath.Base(hdr.Name) == "cloudflared" && hdr.Typeflag == tar.TypeReg {
				f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
				if err != nil {
					return "", err
				}
				_, copyErr := io.Copy(f, io.LimitReader(tr, 150<<20))
				closeErr := f.Close()
				if copyErr != nil || closeErr != nil {
					return "", errors.Join(copyErr, closeErr)
				}
				extracted = true
				break
			}
		}
		if !extracted {
			return "", errors.New("压缩包中未找到 cloudflared 可执行文件")
		}
	} else {
		f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(f, io.LimitReader(resp.Body, 150<<20))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil {
			return "", errors.Join(copyErr, closeErr)
		}
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, localBin); err != nil {
		return "", err
	}
	return localBin, nil
}

func cloudflaredDownloadURL() (url string, isTGZ bool, err error) {
	const base = "https://github.com/cloudflare/cloudflared/releases/latest/download/"
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return base + "cloudflared-darwin-arm64.tgz", true, nil
	case "darwin/amd64":
		return base + "cloudflared-darwin-amd64.tgz", true, nil
	case "linux/amd64":
		return base + "cloudflared-linux-amd64", false, nil
	case "linux/arm64":
		return base + "cloudflared-linux-arm64", false, nil
	case "windows/amd64":
		return base + "cloudflared-windows-amd64.exe", false, nil
	default:
		return "", false, fmt.Errorf("当前系统架构 %s/%s 暂不支持自动下载 cloudflared，请使用自建中转或直连加密模式", runtime.GOOS, runtime.GOARCH)
	}
}
