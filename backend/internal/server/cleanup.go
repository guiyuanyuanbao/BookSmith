package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"booksmith/internal/task"
)

// 书源批量清理：异步任务（同 SearchJob 模式），支持进度轮询与 SSE 推送。

// CleanupJob 一次清理任务。
type CleanupJob struct {
	ID      string
	Mode    string
	Scope   string
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	stats   CleanupStats
}

// CleanupStats 清理进度/结果。
type CleanupStats struct {
	Checked   int          `json:"checked"`
	Total     int          `json:"total"`
	Dead      int          `json:"dead"`
	Alive     int          `json:"alive"`
	Disabled  int          `json:"disabled"`
	Deleted   int          `json:"deleted"`
	ElapsedMS int64        `json:"elapsedMs"`
	Running   bool         `json:"running"`
	Results   []verifyItem `json:"results,omitempty"`
}

type verifyItem struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	ElapsedMS int64  `json:"elapsedMs"`
	Err       string `json:"err,omitempty"`
}

// Snapshot 当前快照（results 截断到最近 200 条避免大响应）。
func (j *CleanupJob) Snapshot() *CleanupStats {
	j.mu.Lock()
	defer j.mu.Unlock()
	cp := j.stats
	cp.Running = j.running
	if cp.Results == nil {
		cp.Results = []verifyItem{}
	}
	if len(cp.Results) > 200 {
		cp.Results = cp.Results[len(cp.Results)-200:]
	}
	return &cp
}

func (j *CleanupJob) update(fn func(*CleanupStats)) {
	j.mu.Lock()
	fn(&j.stats)
	j.mu.Unlock()
}

// startCleanup 启动清理任务（同 scope+mode 进行中则复用）。
func (s *Server) startCleanup(scope, mode string) (*CleanupJob, error) {
	s.app.CleanupMu.Lock()
	defer s.app.CleanupMu.Unlock()

	for _, j := range s.app.CleanupJobs {
		j.mu.Lock()
		running, m, sc := j.running, j.Mode, j.Scope
		j.mu.Unlock()
		if running && m == mode && sc == scope {
			return j, nil
		}
	}

	urls, err := s.collectCleanupTargets(scope)
	if err != nil {
		return nil, err
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("没有可处理的书源")
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &CleanupJob{
		ID:      fmt.Sprintf("c%d", time.Now().UnixNano()),
		Mode:    mode,
		Scope:   scope,
		running: true,
		cancel:  cancel,
		stats:   CleanupStats{Total: len(urls), Running: true},
	}
	s.app.CleanupJobs[job.ID] = job
	// 只保留最近 4 个已完成任务
	for len(s.app.CleanupJobs) > 4 {
		oldest := ""
		for id, j := range s.app.CleanupJobs {
			j.mu.Lock()
			r := j.running
			j.mu.Unlock()
			if !r {
				oldest = id
				break
			}
		}
		if oldest == "" {
			break
		}
		delete(s.app.CleanupJobs, oldest)
	}

	go func() {
		defer cancel()
		s.runCleanup(ctx, job, urls, mode)
	}()
	return job, nil
}

// runCleanup 执行校验循环（并发 16，逐源更新进度并推送 SSE）。
func (s *Server) runCleanup(ctx context.Context, job *CleanupJob, urls []string, mode string) {
	start := time.Now()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)

	for _, u := range urls {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			item := s.verifyOneSource(ctx, u)
			mu.Lock()
			job.update(func(st *CleanupStats) {
				st.Checked++
				st.Results = append(st.Results, item)
				if item.OK {
					st.Alive++
				} else {
					st.Dead++
					switch mode {
					case "delete":
						if derr := s.app.Store.DeleteSource(u); derr == nil {
							st.Deleted++
						}
					default: // disable
						if derr := s.app.Store.SetSourceEnabled(u, false); derr == nil {
							st.Disabled++
						}
					}
				}
				st.ElapsedMS = time.Since(start).Milliseconds()
			})
			mu.Unlock()
			snap := job.Snapshot()
			s.app.Bus.Publish(task.Event{Type: "cleanup.progress", Data: map[string]any{
				"cleanupId": job.ID, "checked": snap.Checked, "total": snap.Total,
				"dead": snap.Dead, "alive": snap.Alive, "disabled": snap.Disabled, "deleted": snap.Deleted,
			}})
		}(u)
	}
	wg.Wait()

	job.mu.Lock()
	job.stats.ElapsedMS = time.Since(start).Milliseconds()
	job.running = false
	job.mu.Unlock()
	s.app.Bus.Publish(task.Event{Type: "cleanup.done", Data: map[string]any{"cleanupId": job.ID}})
}

// verifyOneSource 校验单个书源并落库结果。
func (s *Server) verifyOneSource(ctx context.Context, u string) verifyItem {
	bsAny, err := s.app.Store.GetSource(u)
	if err != nil {
		return verifyItem{URL: u, OK: false, Err: "书源不存在"}
	}
	bs := bsAny
	vStart := time.Now()
	keyword := "我的"
	if bs.RuleSearch != nil && bs.RuleSearch.CheckKeyWord != "" &&
		!strings.Contains(bs.RuleSearch.CheckKeyWord, "http") {
		keyword = strings.TrimSpace(bs.RuleSearch.CheckKeyWord)
	}
	_, serr := s.app.WebBook.SearchOne(ctx, bs, keyword, 1)
	elapsed := time.Since(vStart).Milliseconds()
	ok := serr == nil
	_ = s.app.Store.SetSourceVerified(u, ok, errString(serr), elapsed)
	return verifyItem{URL: u, Name: bs.BookSourceName, OK: ok, ElapsedMS: elapsed, Err: errString(serr)}
}

// collectCleanupTargets 按范围收集目标源 URL。
// scope=="all" → 含禁用源；否则仅启用的源。
func (s *Server) collectCleanupTargets(scope string) ([]string, error) {
	sources, err := s.app.Store.AllSources(scope != "all")
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(sources))
	for _, bs := range sources {
		urls = append(urls, bs.BookSourceURL)
	}
	return urls, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
