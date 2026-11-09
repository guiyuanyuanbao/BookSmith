// BookSmith —— 基于「开源阅读」规则引擎的书籍获取与电子书制作系统。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

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
	)
	flag.Parse()

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

	log.Printf("BookSmith %s 启动于 http://localhost%s", version, *addr)
	log.Printf("数据目录: %s", *dataDir)

	s := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}
	if err := s.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "服务退出:", err)
		os.Exit(1)
	}
}
