// Package web 把构建好的前端（web/dist）打包进 Go 程序，发布版只需要一个文件。
// 先 cd web && npm run build，再 go build ./cmd/frogbot。
// 没构建前端时 web/dist 里只有占位文件 .gitkeep，Go 代码照样能编译和测试。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist 返回打包进程序的前端文件；ok=false 表示构建时还没有 npm run build。
func Dist() (fsys fs.FS, ok bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
