//go:build linux

package rootd

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSocketChecksPeerPID(t *testing.T) {
	s, _ := newServer(t)
	sock := filepath.Join(t.TempDir(), "rootd.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Int64 // Verify 在服务端 goroutine 里读
	allowed.Store(int64(os.Getpid()))
	s.Verify = func(pid, uid int) error {
		if int64(pid) != allowed.Load() || uid != os.Getuid() {
			return errors.New("不是 frog 主进程")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx, ln)
	c := &Client{Socket: sock}
	if resp, err := c.Do(ctx, Request{Op: "status"}); err != nil || !resp.Status.Configured {
		t.Fatalf("own pid must be accepted: %+v %v", resp, err)
	}
	allowed.Store(1) // 假装主进程是别的进程：这次连接必须被拒绝
	if _, err := c.Do(ctx, Request{Op: "status"}); err == nil || !strings.Contains(err.Error(), "拒绝连接") {
		t.Fatalf("foreign pid must be rejected, got %v", err)
	}
}
