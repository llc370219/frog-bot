package web

import "embed"

// DistFS 内嵌编译好的前端静态文件。
//go:embed dist/*
var DistFS embed.FS
