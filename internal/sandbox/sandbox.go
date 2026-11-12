// Package sandbox 用 Linux Landlock 把后台 Agent 的命令（shell、外部 CLI）关在一个文件系统沙箱里：
// 除了受保护的目录（frog 的数据目录：登录数据、会话签名密钥、密钥库、事件日志）以外都能照常读写，
// 受保护目录里只开放明确列出的子目录（例如一键安装的 CLI）。Landlock 不需要 root，
// 配合 systemd 的 NoNewPrivileges 使用；子进程一旦进入沙箱就无法退出，也不能 ptrace 沙箱外的进程。
//
// 用法：frog 主进程把命令改写成 `frogbot sandbox-exec --spec <json> -- 程序 参数…`，
// sandbox-exec 在本进程里启用 Landlock，然后 execve 成目标程序（同一个 PID）。
package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Access 是开放给某个路径的权限。
type Access string

const (
	ReadOnly  Access = "ro" // 读和执行
	ReadWrite Access = "rw"
)

// Rule 在受保护目录里开放一个子路径。
type Rule struct {
	Path   string `json:"path"`
	Access Access `json:"access"`
}

// Spec 描述沙箱：Protect 里的目录默认不可访问，Allow 里的路径例外开放；其余路径全部可读写。
type Spec struct {
	Protect []string `json:"protect"`
	Allow   []Rule   `json:"allow,omitempty"`
}

// Status 是这台机器上沙箱是否可用。
type Status struct {
	Available bool   `json:"available"`
	ABI       int    `json:"abi,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Normalize 把路径整理成绝对路径并解析符号链接，去掉空项。
func (s Spec) Normalize() (Spec, error) {
	var out Spec
	for _, p := range s.Protect {
		if p == "" {
			continue
		}
		abs, err := realPath(p)
		if err != nil {
			return Spec{}, fmt.Errorf("受保护目录 %s：%w", p, err)
		}
		if abs == "/" {
			return Spec{}, errors.New("不能把根目录设为受保护目录")
		}
		out.Protect = append(out.Protect, abs)
	}
	if len(out.Protect) == 0 {
		return Spec{}, errors.New("没有受保护目录")
	}
	for _, r := range s.Allow {
		if r.Path == "" {
			continue
		}
		abs, err := realPath(r.Path)
		if err != nil {
			continue // 还不存在的目录（例如还没装过 CLI）先跳过
		}
		if r.Access != ReadWrite {
			r.Access = ReadOnly
		}
		out.Allow = append(out.Allow, Rule{abs, r.Access})
	}
	return out, nil
}

func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// within 判断 p 是否等于 dir 或在 dir 下面。
func within(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// Wrap 把 cmd 改写成先进沙箱再执行原命令。self 是 frogbot 可执行文件。
func Wrap(cmd *exec.Cmd, self string, spec Spec) error {
	if cmd.Err != nil {
		return nil // 原命令本来就找不到，让它照常报错
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	args := append([]string{self, "sandbox-exec", "--spec", string(raw), "--", cmd.Path}, cmd.Args[1:]...)
	cmd.Path, cmd.Args = self, args
	return nil
}

// Main 是 `frogbot sandbox-exec` 子命令：启用沙箱后 execve 成目标程序，失败时退出码 126。
func Main(args []string) {
	if len(args) < 4 || args[0] != "--spec" || args[2] != "--" {
		fail("用法：frogbot sandbox-exec --spec <json> -- 程序 [参数…]")
	}
	var spec Spec
	if err := json.Unmarshal([]byte(args[1]), &spec); err != nil {
		fail("沙箱配置无效：" + err.Error())
	}
	argv := args[3:]
	path := argv[0]
	if !strings.ContainsRune(path, '/') {
		p, err := exec.LookPath(path)
		if err != nil {
			fail("找不到 " + path)
		}
		path = p
	}
	if err := enter(spec); err != nil {
		fail("无法启用沙箱：" + err.Error())
	}
	if err := execve(path, argv, os.Environ()); err != nil {
		fail(fmt.Sprintf("无法运行 %s：%v", path, err))
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "frog 沙箱："+msg)
	os.Exit(126)
}

// SelfTest 真的启动一个沙箱子进程，确认它读不到受保护目录里的文件。
// 只有通过自检，frog 才认为沙箱已启用（提权功能依赖这一点）。
func SelfTest(self string, spec Spec) error {
	spec, err := spec.Normalize()
	if err != nil {
		return err
	}
	var canaries []string
	for _, d := range spec.Protect {
		p := filepath.Join(d, ".sandbox-canary")
		if err := os.WriteFile(p, []byte("frog"), 0o600); err != nil {
			return fmt.Errorf("沙箱自检无法写入 %s：%w", p, err)
		}
		defer os.Remove(p)
		canaries = append(canaries, p)
	}
	cmd := exec.Command(self, append([]string{"sandbox-check"}, canaries...)...)
	if err := Wrap(cmd, self, spec); err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("沙箱自检失败：%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CheckMain 是 `frogbot sandbox-check 文件…`：在沙箱里运行，确认每个文件都读不到，读到了就退出码 1。
func CheckMain(files []string) {
	for _, p := range files {
		if _, err := os.ReadFile(p); err == nil {
			fmt.Fprintln(os.Stderr, "沙箱里仍能读取 "+p)
			os.Exit(1)
		} else if !errors.Is(err, os.ErrPermission) {
			fmt.Fprintf(os.Stderr, "读取 %s 的结果不是「没有权限」：%v\n", p, err)
			os.Exit(1)
		}
	}
	os.Exit(0)
}
