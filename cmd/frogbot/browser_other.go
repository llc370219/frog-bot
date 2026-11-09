//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

var errNoDisplay = errors.New("没有图形界面")

func openBrowser(url string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url).Run()
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errNoDisplay // 服务器 / SSH 里只打印地址
	}
	return exec.Command("xdg-open", url).Run()
}
