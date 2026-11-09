package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"booksmith/internal/export"
	"booksmith/internal/model"
	"booksmith/internal/source"
	"booksmith/internal/store"
	"booksmith/internal/task"
	"booksmith/internal/webbook"
)

// App 应用上下文（聚合各层服务，直接引用具体类型）。
type App struct {
	SourceMgr *source.Manager
	WebBook   *webbook.Service
	TaskMgr   *task.Manager
	Store     *store.Store
	StaticFS  http.FileSystem
	DataDir   string
	Version   string
	Bus       *task.EventBus

	// 搜索任务与封面缓存
	SearchJobs  map[string]*SearchJob
	SearchMu    sync.Mutex
	CleanupJobs map[string]*CleanupJob
	CleanupMu   sync.Mutex
	CoverCache  map[string]coverCacheEntry
	CoverClient *http.Client
}

// FindEnabledSources 启用书源列表（webbook.Search 输入）。
func (a *App) FindEnabledSources() ([]*model.BookSource, error) {
	return a.Store.AllSources(true)
}

// Routes 注册全部路由（Go 1.22+ ServeMux 模式）。
func (s *Server) Routes(mux *http.ServeMux) {
	// 健康检查
	mux.HandleFunc("GET /api/health", s.handleHealth)

	// 书源
	mux.HandleFunc("GET /api/sources", s.handleListSources)
	mux.HandleFunc("POST /api/sources/import", s.handleImportSources)
	mux.HandleFunc("POST /api/sources/sync", s.handleSyncRepo)
	mux.HandleFunc("GET /api/sources/repos", s.handleRepos)
	mux.HandleFunc("GET /api/sources/export", s.handleExportSources)
	mux.HandleFunc("PATCH /api/sources/{url}", s.handlePatchSource)
	mux.HandleFunc("DELETE /api/sources/{url}", s.handleDeleteSource)

	// 净化规则
	mux.HandleFunc("GET /api/replace", s.handleListReplace)
	mux.HandleFunc("POST /api/replace/import", s.handleImportReplace)
	mux.HandleFunc("POST /api/replace", s.handleSaveReplace)
	mux.HandleFunc("DELETE /api/replace/{id}", s.handleDeleteReplace)

	// 搜索（异步任务 + 进度）
	mux.HandleFunc("POST /api/search", s.handleSearchStart)
	mux.HandleFunc("GET /api/search/{id}", s.handleSearchGet)
	mux.HandleFunc("DELETE /api/search/{id}", s.handleSearchCancel)

	// 封面代理（防盗链）
	mux.HandleFunc("GET /api/cover", s.handleCover)

	// 书源校验与调试
	mux.HandleFunc("POST /api/sources/verify", s.handleVerifySources)
	mux.HandleFunc("POST /api/sources/cleanup", s.handleCleanupStart)
	mux.HandleFunc("GET /api/sources/cleanup/{id}", s.handleCleanupGet)
	mux.HandleFunc("DELETE /api/sources/cleanup/{id}", s.handleCleanupCancel)
	mux.HandleFunc("GET /api/sources/debug", s.handleSourceDebug)

	// 净化规则测试
	mux.HandleFunc("POST /api/replace/test", s.handleTestReplace)

	// 任务失败章节
	mux.HandleFunc("GET /api/tasks/{id}/failures", s.handleTaskFailures)

	// 搜索与书架
	mux.HandleFunc("GET /api/books", s.handleListBooksWithStats)
	mux.HandleFunc("GET /api/books/grouped", s.handleListBookGroups)
	mux.HandleFunc("GET /api/books/{id}", s.handleGetBook)
	mux.HandleFunc("DELETE /api/books/{id}", s.handleDeleteBook)
	mux.HandleFunc("GET /api/books/{id}/stats", s.handleBookStats)
	mux.HandleFunc("POST /api/books/{id}/download", s.handleBookDownload)
	mux.HandleFunc("POST /api/books/{id}/export-now", s.handleExportNow)
	mux.HandleFunc("POST /api/books/{id}/cover", s.handleCoverUpload)
	mux.HandleFunc("GET /api/books/{id}/cover", s.handleCoverGet)
	mux.HandleFunc("DELETE /api/books/{id}/cover", s.handleCoverDelete)
	mux.HandleFunc("GET /api/books/{id}/toc", s.handleBookToc)
	mux.HandleFunc("GET /api/books/{id}/chapters/{index}", s.handleChapter)

	// 任务
	mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("POST /api/tasks/{id}/pause", s.handleTaskPause)
	mux.HandleFunc("POST /api/tasks/{id}/resume", s.handleTaskResume)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.handleTaskCancel)
	mux.HandleFunc("POST /api/tasks/{id}/retry", s.handleTaskRetry)
	mux.HandleFunc("POST /api/tasks/clear-finished", s.handleClearFinishedTasks)

	// 导出文件
	mux.HandleFunc("GET /api/exports", s.handleListExports)
	mux.HandleFunc("DELETE /api/exports/{id}", s.handleDeleteExport)
	mux.HandleFunc("GET /api/files/{name}", s.handleDownloadFile)

	// 搜索历史
	mux.HandleFunc("GET /api/search-history", s.handleGetSearchHistory)
	mux.HandleFunc("POST /api/search-history", s.handleAddSearchHistory)
	mux.HandleFunc("DELETE /api/search-history", s.handleClearSearchHistory)

	// 设置
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)

	// 批量操作
	mux.HandleFunc("POST /api/sources/batch", s.handleSourceBatch)
	mux.HandleFunc("POST /api/books/batch", s.handleBookBatch)

	// 概览统计
	mux.HandleFunc("GET /api/overview", s.handleOverview)

	// SSE 事件
	mux.HandleFunc("GET /api/events", s.handleEvents)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{"status": "ok", "version": s.app.Version, "time": time.Now().UnixMilli()})
}

// ---------------- 书源 ----------------

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("keyword")
	enabled := queryInt(r, "enabled", -1)
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	items, total, err := s.app.Store.ListSources(keyword, enabled, limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{"items": items, "total": total})
}

func (s *Server) handleImportSources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL  string `json:"url"`
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "请求体错误: "+err.Error())
		return
	}
	var res any
	var err error
	if req.URL != "" {
		res, err = s.app.SourceMgr.ImportURL(req.URL)
	} else if req.Text != "" {
		res, err = s.app.SourceMgr.ImportText(req.Text, "手动导入")
	} else {
		writeErr(w, 400, "需要 url 或 text 字段")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, res)
}

func (s *Server) handleSyncRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil || req.URL == "" {
		writeErr(w, 400, "需要 url 字段")
		return
	}
	for _, repo := range s.app.SourceMgr.BuiltinRepos() {
		if repo.URL == req.URL {
			writeOK(w, s.app.SourceMgr.SyncRepo(repo))
			return
		}
	}
	writeOK(w, s.app.SourceMgr.SyncRepo(model.SourceRepo{Name: "自定义", URL: req.URL, Kind: "bookSource"}))
}

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	writeOK(w, s.app.SourceMgr.BuiltinRepos())
}

func (s *Server) handleExportSources(w http.ResponseWriter, r *http.Request) {
	enabledOnly := r.URL.Query().Get("enabledOnly") == "1"
	data, err := s.app.SourceMgr.ExportSources(enabledOnly)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=book_sources.json")
	_, _ = w.Write(data)
}

func (s *Server) handlePatchSource(w http.ResponseWriter, r *http.Request) {
	url := r.PathValue("url")
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Enabled == nil {
		writeErr(w, 400, "需要 enabled 字段")
		return
	}
	if err := s.app.Store.SetSourceEnabled(url, *req.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, nil)
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	url := r.PathValue("url")
	if err := s.app.Store.DeleteSource(url); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, nil)
}

// ---------------- 净化规则 ----------------

func (s *Server) handleListReplace(w http.ResponseWriter, r *http.Request) {
	enabledOnly := r.URL.Query().Get("enabledOnly") == "1"
	rules, err := s.app.Store.ListReplaceRules(enabledOnly)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, rules)
}

func (s *Server) handleImportReplace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL  string `json:"url"`
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var res any
	var err error
	if req.URL != "" {
		res, err = s.app.SourceMgr.ImportReplaceURL(req.URL)
	} else if req.Text != "" {
		res, err = s.app.SourceMgr.ImportReplaceText(req.Text)
	} else {
		writeErr(w, 400, "需要 url 或 text")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, res)
}

func (s *Server) handleSaveReplace(w http.ResponseWriter, r *http.Request) {
	var rule model.ReplaceRule
	if err := readJSON(r, &rule); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if rule.Pattern == "" {
		writeErr(w, 400, "pattern 不能为空")
		return
	}
	if err := s.app.Store.UpsertReplaceRule(&rule); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, rule)
}

func (s *Server) handleDeleteReplace(w http.ResponseWriter, r *http.Request) {
	id := queryInt64(r, "id", 0)
	if ps := r.PathValue("id"); ps != "" {
		var err error
		id, err = strconv.ParseInt(ps, 10, 64)
		if err != nil {
			writeErr(w, 400, "id 非法")
			return
		}
	}
	if err := s.app.Store.DeleteReplaceRule(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, nil)
}

// ---------------- 搜索 ----------------

// ---------------- 书架 ----------------

func (s *Server) handleListBooks(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("keyword")
	items, total, err := s.app.Store.ListBooks(keyword, queryInt(r, "limit", 50), queryInt(r, "offset", 0))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{"items": items, "total": total})
}

func (s *Server) handleGetBook(w http.ResponseWriter, r *http.Request) {
	id := queryInt64(r, "id", 0)
	if ps := r.PathValue("id"); ps != "" {
		id, _ = strconv.ParseInt(ps, 10, 64)
	}
	book, err := s.app.Store.GetBook(id)
	if err != nil {
		writeErr(w, 404, "书籍不存在")
		return
	}
	writeOK(w, book)
}

func (s *Server) handleDeleteBook(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.app.Store.DeleteBook(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, nil)
}

func (s *Server) handleBookToc(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	withContent := r.URL.Query().Get("content") == "1"
	chapters, err := s.app.Store.ListChapters(id, withContent)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, chapters)
}

func (s *Server) handleChapter(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	index, _ := strconv.Atoi(r.PathValue("index"))
	ch, err := s.app.Store.GetChapter(id, index)
	if err != nil {
		writeErr(w, 404, "章节不存在")
		return
	}
	writeOK(w, ch)
}

// ---------------- 任务 ----------------

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.app.TaskMgr.ListTasks(queryInt(r, "limit", 50))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, tasks)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BookURL     string   `json:"bookUrl"`
		BookName    string   `json:"bookName"`
		Author      string   `json:"author"`
		TocURL      string   `json:"tocUrl"`
		SourceURL   string   `json:"sourceUrl"`
		CoverURL    string   `json:"coverUrl"`
		Intro       string   `json:"intro"`
		Formats     []string `json:"formats"`
		RangeStart  int      `json:"rangeStart"`
		RangeEnd    int      `json:"rangeEnd"` // -1 到末尾
		Concurrency int      `json:"concurrency"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.BookURL == "" || req.SourceURL == "" {
		writeErr(w, 400, "需要 bookUrl 与 sourceUrl")
		return
	}
	if len(req.Formats) == 0 {
		req.Formats = []string{"txt", "epub"}
	}
	for _, f := range req.Formats {
		if !export.ValidFormat(f) {
			writeErr(w, 400, "不支持的格式: "+f)
			return
		}
	}
	book := &model.Book{
		BookURL:  req.BookURL,
		Name:     req.BookName,
		Author:   req.Author,
		TocURL:   req.TocURL,
		CoverURL: req.CoverURL,
		Intro:    req.Intro,
	}
	// 书源
	bs, err := s.app.Store.GetSource(req.SourceURL)
	if err != nil {
		writeErr(w, 404, "书源不存在: "+req.SourceURL)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	taskID, existed, err := s.app.TaskMgr.Create(ctx, book, bs, req.Formats, req.RangeStart, req.RangeEnd, req.Concurrency)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{"taskId": taskID, "existing": existed})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	t, err := s.app.TaskMgr.GetTask(id)
	if err != nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	writeOK(w, t)
}

func (s *Server) handleTaskAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var err error
		switch action {
		case "pause":
			err = s.app.TaskMgr.Pause(id)
		case "resume":
			err = s.app.TaskMgr.Resume(id)
		case "cancel":
			err = s.app.TaskMgr.Cancel(id)
		case "retry":
			err = s.app.TaskMgr.RetryFailed(id)
		}
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeOK(w, nil)
	}
}

func (s *Server) handleTaskPause(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction("pause")(w, r)
}
func (s *Server) handleTaskResume(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction("resume")(w, r)
}
func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction("cancel")(w, r)
}
func (s *Server) handleTaskRetry(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction("retry")(w, r)
}

// ---------------- 导出文件 ----------------

func (s *Server) handleListExports(w http.ResponseWriter, r *http.Request) {
	if bookID := queryInt64(r, "bookId", 0); bookID > 0 {
		exports, err := s.app.Store.ListExportsByBook(bookID, queryInt(r, "limit", 100))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeOK(w, exports)
		return
	}
	exports, err := s.app.Store.ListExports(queryInt(r, "limit", 100))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, exports)
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name")) // 防目录穿越
	path := filepath.Join(s.app.TaskMgr.OutDir(), name)
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 404, "文件不存在")
		return
	}
	ct := "application/octet-stream"
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt":
		ct = "text/plain; charset=utf-8"
	case ".epub":
		ct = "application/epub+zip"
	case ".pdf":
		ct = "application/pdf"
	case ".html":
		ct = "text/html; charset=utf-8"
	case ".md":
		ct = "text/markdown; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(name))
	_, _ = w.Write(data)
}

func urlEscape(s string) string {
	r := strings.NewReplacer("%", "%25", " ", "%20", `"`, "%22", "(", "%28", ")", "%29")
	return r.Replace(s)
}
