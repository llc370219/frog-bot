//go:build !linux

package sandbox

import "errors"

// Probe：Landlock 只有 Linux 有。
func Probe() Status { return Status{Reason: "只有 Linux 支持沙箱（Landlock）"} }

func enter(Spec) error { return errors.New("只有 Linux 支持沙箱") }

func execve(string, []string, []string) error { return errors.New("只有 Linux 支持沙箱") }

// SetNonDumpable 在非 Linux 系统上什么也不做。
func SetNonDumpable() error { return nil }
