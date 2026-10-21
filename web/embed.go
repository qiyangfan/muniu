// Package web 嵌入控制台构建产物（dist），由网关 /console/* 提供，单二进制自带控制台。
// 未执行 pnpm build 的纯 Go 构建中，dist 以占位文件存在，路由仍可用。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler 返回控制台静态资源；未知路径回退 index.html，避免 FileServer 重定向循环。
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/console/")
		if p == "" || p == "/" {
			p = "index.html"
		}
		b, readErr := fs.ReadFile(sub, p)
		if readErr != nil {
			b, readErr = fs.ReadFile(sub, "index.html") // SPA 回退
			if readErr != nil {
				http.NotFound(w, r)
				return
			}
			p = "index.html"
		}
		switch {
		case strings.HasSuffix(p, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(p, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case strings.HasSuffix(p, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(p, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
		}
		_, _ = w.Write(b)
	})
}
