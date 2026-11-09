// Package web 内嵌前端构建产物（vite outDir → backend/web/dist）。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler 返回前端静态资源处理器（SPA 回退到 index.html）。
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// API 请求不处理
		if len(path) >= 4 && path[:4] == "/api" {
			http.NotFound(w, r)
			return
		}
		// 静态文件存在则直出，否则回退 index.html（SPA 路由）
		if path != "/" {
			if f, err := sub.Open(strings.TrimPrefix(path, "/")); err == nil {
				stat, serr := f.Stat()
				f.Close()
				if serr == nil && !stat.IsDir() {
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}
		// index.html
		index, err := fs.ReadFile(distFS, "dist/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	}), nil
}
