//go:build unix

package rootd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// state 是 /etc/frogbot/rootd.json 的内容（root 0600）。
type state struct {
	Hash    string `json:"password_hash"`
	Enabled bool   `json:"enabled"`
}

// Server 是 rootd 的服务端。
type Server struct {
	StatePath string
	AuditPath string
	// Verify 检查连进来的进程是不是 frog 主进程；返回错误就拒绝。
	Verify func(pid, uid int) error
	Now    func() time.Time

	mu         sync.Mutex
	st         state
	grantUntil time.Time
	oneShot    time.Time // 只放行下一条命令，到这个时间失效
	fails      int
	lockedTill time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Load 读取状态文件；不存在时是「未配置」。
func (s *Server) Load() error {
	data, err := os.ReadFile(s.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.st)
}

func (s *Server) save() error {
	data, _ := json.MarshalIndent(s.st, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.StatePath), 0o755); err != nil {
		return err
	}
	tmp := s.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.StatePath)
}

// SetHash 直接设置密码哈希（root 在命令行同步密码时用）。
func (s *Server) SetHash(hash string, enable bool) error {
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return fmt.Errorf("不是有效的 bcrypt 哈希：%w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Hash = hash
	if enable {
		s.st.Enabled = true
	}
	return s.save()
}

func (s *Server) status() Status {
	now := s.now()
	st := Status{Configured: s.st.Hash != "", Enabled: s.st.Enabled}
	if now.Before(s.grantUntil) {
		st.GrantUntil, st.GrantLeftSec = s.grantUntil, int(s.grantUntil.Sub(now).Seconds())+1
	}
	st.Pending = now.Before(s.oneShot)
	if now.Before(s.lockedTill) {
		st.LockedUntil, st.LockedLeftSec = s.lockedTill, int(s.lockedTill.Sub(now).Seconds())+1
	}
	return st
}

// checkPassword 核对密码，带锁定。调用时持有 s.mu。
func (s *Server) checkPassword(pw string) error {
	now := s.now()
	if now.Before(s.lockedTill) {
		return fmt.Errorf("密码输错太多次，请 %d 分钟后再试", int(s.lockedTill.Sub(now).Minutes())+1)
	}
	if s.st.Hash == "" {
		return errors.New("提权助手还没有设置密码：请在服务器上重新运行安装命令")
	}
	if pw == "" {
		return errors.New("请输入登录密码")
	}
	if bcrypt.CompareHashAndPassword([]byte(s.st.Hash), []byte(pw)) != nil {
		s.fails++
		if s.fails >= 5 {
			s.fails, s.lockedTill = 0, now.Add(15*time.Minute)
			return errors.New("密码输错 5 次，提权已锁定 15 分钟")
		}
		return errors.New("密码不对")
	}
	s.fails = 0
	return nil
}

// Handle 处理一个请求（已经通过对端检查）。
func (s *Server) Handle(ctx context.Context, req Request) Response {
	s.mu.Lock()
	switch req.Op {
	case "status":
		defer s.mu.Unlock()
		return Response{OK: true, Status: s.status()}
	case "revoke":
		s.grantUntil, s.oneShot = time.Time{}, time.Time{}
		defer s.mu.Unlock()
		return Response{OK: true, Status: s.status()}
	case "disable":
		s.st.Enabled, s.grantUntil, s.oneShot = false, time.Time{}, time.Time{}
		err := s.save()
		defer s.mu.Unlock()
		return reply(s.status(), err)
	case "enable":
		defer s.mu.Unlock()
		if err := s.checkPassword(req.Password); err != nil {
			return reply(s.status(), err)
		}
		s.st.Enabled = true
		return reply(s.status(), s.save())
	case "set_password":
		defer s.mu.Unlock()
		if err := s.checkPassword(req.Old); err != nil {
			return reply(s.status(), err)
		}
		if len(req.New) < 8 {
			return reply(s.status(), errors.New("新密码太短"))
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
		if err != nil {
			return reply(s.status(), err)
		}
		s.st.Hash = string(hash)
		s.grantUntil, s.oneShot = time.Time{}, time.Time{}
		return reply(s.status(), s.save())
	case "authorize":
		defer s.mu.Unlock()
		if !s.st.Enabled {
			return reply(s.status(), errors.New("提权已关闭，请先在设置里开启"))
		}
		now := s.now()
		if req.Password == "" {
			// 不输密码：只有免密窗口还开着时才行（每条命令仍要用户点批准）
			if now.Before(s.grantUntil) {
				s.oneShot = now.Add(5 * time.Minute)
				return Response{OK: true, Status: s.status()}
			}
			return reply(s.status(), errors.New("请输入登录密码"))
		}
		if err := s.checkPassword(req.Password); err != nil {
			return reply(s.status(), err)
		}
		s.oneShot = now.Add(5 * time.Minute)
		if req.Minutes > 0 {
			s.grantUntil = now.Add(min(time.Duration(req.Minutes)*time.Minute, MaxGrant))
		}
		return Response{OK: true, Status: s.status()}
	case "run":
		now := s.now()
		if !s.st.Enabled {
			s.mu.Unlock()
			return reply(s.status(), errors.New("提权已关闭"))
		}
		// 每条命令都要用掉一次授权：用户批准时 frog 会先 authorize，再 run。
		if !now.Before(s.oneShot) {
			st := s.status()
			s.mu.Unlock()
			return reply(st, errors.New("这条命令没有得到授权"))
		}
		s.oneShot = time.Time{}
		st := s.status()
		s.mu.Unlock()
		out, code, err := runAsRoot(ctx, req)
		s.audit(req, code, err)
		resp := reply(st, err)
		resp.Output, resp.ExitCode = out, code
		return resp
	}
	s.mu.Unlock()
	return Response{Error: "未知的操作：" + req.Op}
}

func reply(st Status, err error) Response {
	if err != nil {
		return Response{Error: err.Error(), Status: st}
	}
	return Response{OK: true, Status: st}
}

const maxOutput = 1 << 20

type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if left := maxOutput - c.Len(); left > 0 {
		c.Buffer.Write(p[:min(len(p), left)])
	}
	return len(p), nil
}

func runAsRoot(ctx context.Context, req Request) (string, int, error) {
	if strings.TrimSpace(req.Command) == "" {
		return "", -1, errors.New("命令为空")
	}
	to := time.Duration(req.TimeoutSec) * time.Second
	if to <= 0 {
		to = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, min(to, 30*time.Minute))
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", req.Command)
	if _, err := os.Stat("/bin/bash"); err != nil {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", req.Command)
	}
	// 工作目录：请求里给了能进入的绝对路径就用它，否则 /root，进不去（不是 root 运行时）再退回 /。
	cmd.Dir = "/"
	for _, d := range []string{req.Dir, "/root"} {
		if filepath.IsAbs(d) && enterable(d) {
			cmd.Dir = d
			break
		}
	}
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root",
		"LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive", "USER=root", "LOGNAME=root"}
	cmd.Stdin = nil
	var out capped
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return out.String(), -1, fmt.Errorf("命令超时或被取消")
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode(), nil
	}
	if err != nil {
		return out.String(), -1, err
	}
	return out.String(), 0, nil
}

// enterable 判断当前进程能不能 cd 进这个目录（存在、是目录、有执行权限）。
func enterable(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir() && syscall.Access(dir, 1) == nil // 1 = X_OK
}

func (s *Server) audit(req Request, code int, err error) {
	if s.AuditPath == "" {
		return
	}
	rec := map[string]any{"time": s.now().Format(time.RFC3339), "command": req.Command, "dir": req.Dir,
		"task": req.Task, "approval": req.Approval, "exit_code": code}
	if err != nil {
		rec["error"] = err.Error()
	}
	line, _ := json.Marshal(rec)
	f, ferr := os.OpenFile(s.AuditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if ferr != nil {
		log.Printf("写审计日志失败：%v", ferr)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// Serve 在 unix socket 上提供服务，直到 ctx 结束。
func (s *Server) Serve(ctx context.Context, ln *net.UnixListener) error {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, conn)
	}
}

func (s *Server) serveConn(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	pid, uid, err := peerCred(conn)
	if err == nil && s.Verify != nil {
		err = s.Verify(pid, uid)
	}
	enc := json.NewEncoder(conn)
	if err != nil {
		log.Printf("拒绝连接（pid %d uid %d）：%v", pid, uid, err)
		_ = enc.Encode(Response{Error: "拒绝连接：" + err.Error()})
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(conn, 1<<20)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = enc.Encode(Response{Error: "请求格式不对"})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	_ = enc.Encode(s.Handle(ctx, req))
}
