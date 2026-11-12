//go:build !unix

package main

func rootdMain([]string) { fatal("提权助手只支持 Linux") }

func syncRootdAfterReset(string) {}
