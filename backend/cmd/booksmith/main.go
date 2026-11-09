// BookSmith —— 基于「开源阅读」规则引擎的书籍获取与电子书制作系统。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"booksmith/internal/export"
	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/server"
	"booksmith/internal/source"
	"booksmith/internal/store"
	"booksmith/internal/task"
	"booksmith/internal/webbook"
	"booksmith/web"
)

var version = "1.0.0"

func main() {
	var (
		addr    = flag.String("addr", ":8484", "监听地址")
		dataDir = flag.String("data", "", "数据目录（默认 ./data）")
		apiKey  = flag.String("api-key", "", "API 密钥（公网部署用；也可用环境变量 BOOKSMITH_API_KEY）")
	)
	flag.Parse()

	if *apiKey == "" {
		*apiKey = os.Getenv("BOOKSMITH_API_KEY")
	}
	if *apiKey != "" {
		log.Printf("鉴权已启用（API Key 保护全部接口）")
	}

	if *dataDir == "" {
		*dataDir = "data"
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	// 存储
	st, err := store.Open(filepath.Join(*dataDir, "booksmith.db"))
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	// HTTP 客户端与 JS 环境
	client := httpx.NewClient()
	env := jsenv.New(client, *dataDir)

	// 获取服务
	svc := webbook.New(client, env)

	// 书源管理
	srcMgr := source.New(st, client)

	// 净化规则提供器（实时读取启用规则）
	svc.ReplaceProvider = func() []*model.ReplaceRule {
		rules, err := st.ListReplaceRules(true)
		if err != nil {
			return nil
		}
		return rules
	}

	// 任务管理器
	bus := task.NewEventBus()
	taskMgr := task.New(st, svc, bus, filepath.Join(*dataDir, "downloads"))
	// 封面缓存提供器（app 创建后装配，导出与代理共用磁盘缓存）
	var coverProvider func(rawURL string) *export.CoverImage
	taskMgr.CoverProvider = func(rawURL string) *export.CoverImage {
		if coverProvider == nil {
			return nil
		}
		return coverProvider(rawURL)
	}
	// 手动上传封面读取（导出用）：data/uploads/{id}.ext，Book.LocalCover 指向文件名
	taskMgr.LocalCover = func(bookID int64) *export.CoverImage {
		b, err := st.GetBook(bookID)
		if err != nil || b.LocalCover == "" {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(*dataDir, "uploads", filepath.Base(b.LocalCover)))
		if err != nil || len(data) == 0 {
			return nil
		}
		ct := export.SniffImageMime(data)
		ext := ".jpg"
		switch {
		case strings.Contains(ct, "png"):
			ext = ".png"
		case strings.Contains(ct, "gif"):
			ext = ".gif"
		case strings.Contains(ct, "webp"):
			ext = ".webp"
		}
		return &export.CoverImage{Data: data, MimeType: ct, Ext: ext}
	}

	app := &server.App{
		SourceMgr:   srcMgr,
		WebBook:     svc,
		TaskMgr:     taskMgr,
		Store:       st,
		DataDir:     *dataDir,
		Version:     version,
		Bus:         bus,
		SearchJobs:  map[string]*server.SearchJob{},
		CleanupJobs: map[string]*server.CleanupJob{},
	}
	app.StartCoverProxy()
	coverProvider = func(rawURL string) *export.CoverImage {
		if d, m, ok := app.CachedCoverImage(rawURL); ok {
			ext := ".jpg"
			switch {
			case strings.Contains(m, "png"):
				ext = ".png"
			case strings.Contains(m, "gif"):
				ext = ".gif"
			case strings.Contains(m, "webp"):
				ext = ".webp"
			}
			return &export.CoverImage{Data: d, MimeType: m, Ext: ext}
		}
		return nil
	}

	// 静态资源
	staticHandler, err := web.Handler()
	if err != nil {
		log.Fatalf("加载前端资源失败: %v", err)
	}
	app.StaticFS = nil

	mux := http.NewServeMux()
	srv := server.New(app)
	srv.Routes(mux)
	mux.Handle("/", staticHandler)

	// 鉴权：密钥未配置时完全放行（本地使用）；配置后保护页面与全部 API
	auth := server.NewAuth(*apiKey)
	mux.HandleFunc("POST /api/auth/login", auth.LoginHandler)
	mux.HandleFunc("GET /api/auth/status", auth.StatusHandler)
	handler := auth.Middleware(mux)

	log.Printf("BookSmith %s 启动于 http://localhost%s", version, *addr)
	log.Printf("数据目录: %s", *dataDir)

	s := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	if err := s.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "服务退出:", err)
		os.Exit(1)
	}
}
