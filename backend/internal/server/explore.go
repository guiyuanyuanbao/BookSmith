// explore.go 发现页 API：启用发现的源列表 + 异步聚合抓取任务（仿 SearchJob 模式）。
package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"booksmith/internal/model"
	"booksmith/internal/task"
)

// ExploreJob 一次发现聚合任务。
type ExploreJob struct {
	ID string
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	Started time.Time
	Books   []*model.ExploreBook
	Errs    []string
	Err     string
}

// Snapshot 任务快照（Books/Errs 返回拷贝：并发 GET /api/explore/{id}
// 各自对共享 slice 做 sort 会数据竞态，必须返回副本）。
func (j *ExploreJob) Snapshot() (books []*model.ExploreBook, errs []string, running bool, err string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	books = make([]*model.ExploreBook, len(j.Books))
	copy(books, j.Books)
	errs = append([]string(nil), j.Errs...)
	return books, errs, j.running, j.Err
}

// handleExploreSources 列出启用了发现的源 + 各源分类。
func (s *Server) handleExploreSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.app.FindEnabledSources()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type sourceKinds struct {
		SourceURL  string             `json:"sourceUrl"`
		SourceName string             `json:"sourceName"`
		Group      string             `json:"group,omitempty"`
		Kinds      []model.ExploreKind `json:"kinds"`
	}
	out := make([]sourceKinds, 0, 8)
	for _, bs := range sources {
		kinds := s.app.WebBook.ExploreKinds(bs)
		if len(kinds) == 0 {
			continue
		}
		out = append(out, sourceKinds{
			SourceURL:  bs.BookSourceURL,
			SourceName: bs.BookSourceName,
			Group:      bs.BookSourceGroup,
			Kinds:      kinds,
		})
	}
	writeOK(w, map[string]any{"sources": out})
}

// handleExploreStart 启动发现聚合任务。
// body: {sourceUrls: ["源URL"], kinds: ["分类URL"], page: 1} —— sourceUrls 与 kinds 一一对应。
func (s *Server) handleExploreStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceURLs []string `json:"sourceUrls"`
		Kinds      []string `json:"kinds"`
		Page       int      `json:"page"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(req.SourceURLs) == 0 || len(req.SourceURLs) != len(req.Kinds) {
		writeErr(w, 400, "sourceUrls 与 kinds 需一一对应且非空")
		return
	}
	if req.Page < 1 {
		req.Page = 1
	}
	if len(req.SourceURLs) > 20 {
		req.SourceURLs = req.SourceURLs[:20]
		req.Kinds = req.Kinds[:20]
	}
	// 取出源对象（仅启用源）
	all, err := s.app.FindEnabledSources()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	byURL := map[string]*model.BookSource{}
	for _, bs := range all {
		byURL[bs.BookSourceURL] = bs
	}
	sources := make([]*model.BookSource, 0, len(req.SourceURLs))
	kinds := make([]string, 0, len(req.SourceURLs))
	for i, u := range req.SourceURLs {
		bs, ok := byURL[u]
		if !ok {
			continue
		}
		sources = append(sources, bs)
		kinds = append(kinds, req.Kinds[i])
	}
	if len(sources) == 0 {
		writeErr(w, 400, "没有匹配的启用源")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &ExploreJob{
		ID:      fmt.Sprintf("e%d", time.Now().UnixNano()),
		running: true,
		cancel:  cancel,
		Started: time.Now(),
	}
	s.app.ExploreMu.Lock()
	s.app.ExploreJobs[job.ID] = job
	for len(s.app.ExploreJobs) > 6 {
		oldest := ""
		for id, j := range s.app.ExploreJobs {
			j.mu.Lock()
			ru := j.running
			j.mu.Unlock()
			if !ru {
				oldest = id
				break
			}
		}
		if oldest == "" {
			break
		}
		delete(s.app.ExploreJobs, oldest)
	}
	s.app.ExploreMu.Unlock()

	go func() {
		defer cancel()
		// panic 兜底：job.running 永久 true 会挤占保留名额并让前端永远转圈
		defer func() {
			if r := recover(); r != nil {
				job.mu.Lock()
				job.Err = fmt.Sprintf("内部异常: %v", r)
				job.running = false
				job.mu.Unlock()
				s.app.Bus.Publish(task.Event{Type: "explore.done", Data: map[string]any{"exploreId": job.ID, "count": 0}})
			}
		}()
		books, errs := s.app.WebBook.Explore(ctx, sources, kinds, req.Page)
		job.mu.Lock()
		job.Books = books
		job.Errs = errs
		if len(books) == 0 && len(errs) > 0 {
			job.Err = strings.Join(errs, "; ")
		}
		job.running = false
		job.mu.Unlock()
		s.app.Bus.Publish(task.Event{Type: "explore.done", Data: map[string]any{"exploreId": job.ID, "count": len(books)}})
	}()
	writeOK(w, map[string]any{"exploreId": job.ID, "total": len(sources)})
}

// handleExploreGet 查询发现任务结果。
func (s *Server) handleExploreGet(w http.ResponseWriter, r *http.Request) {
	s.app.ExploreMu.Lock()
	job := s.app.ExploreJobs[r.PathValue("id")]
	s.app.ExploreMu.Unlock()
	if job == nil {
		writeErr(w, 404, "发现任务不存在或已过期")
		return
	}
	books, errs, running, errStr := job.Snapshot()
	sort.SliceStable(books, func(i, j int) bool {
		return books[i].RespondTime < books[j].RespondTime
	})
	writeOK(w, map[string]any{
		"books": books, "errors": errs, "running": running, "err": errStr,
		"elapsedMs": time.Since(job.Started).Milliseconds(),
	})
}

// handleExploreCancel 取消发现任务。
func (s *Server) handleExploreCancel(w http.ResponseWriter, r *http.Request) {
	s.app.ExploreMu.Lock()
	job := s.app.ExploreJobs[r.PathValue("id")]
	s.app.ExploreMu.Unlock()
	if job == nil {
		writeErr(w, 404, "发现任务不存在或已过期")
		return
	}
	job.cancel()
	writeOK(w, nil)
}
