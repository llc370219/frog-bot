//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

func openBrowser(url string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url).Run()
	}
	return exec.Command("xdg-open", url).Run()
}
