// sync_job.go 书源广场异步同步任务：大合集（最多 1.6 万源）同步耗时，
// 复用 cleanup.go 的 Job 模式——立即返回 jobId，进度轮询/SSE 推送，支持取消。
package source

import (
	"context"
	"fmt"
	"sync"
	"time"

	"booksmith/internal/task"
)

// SyncJob 一次仓库同步任务。
type SyncJob struct {
	ID       string
	RepoName string
	URL      string
	Kind     string
	Cleanup  bool

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	stats   SyncProgress
}

// SyncProgress 同步进度/结果快照。
type SyncProgress struct {
	RepoName  string `json:"repoName"`
	Total     int    `json:"total"`
	Done      int    `json:"done"`
	Inserted  int    `json:"inserted"`
	Updated   int    `json:"updated"`
	Removed   int    `json:"removed,omitempty"`
	Running   bool   `json:"running"`
	Finished  bool   `json:"finished"`
	Err       string `json:"err,omitempty"`
	ElapsedMS int64  `json:"elapsedMs"`
}

// Snapshot 当前快照（Running 取自 stats——runSyncJob 完成时已置 false；j.running 仅表示 goroutine 存活）。
func (j *SyncJob) Snapshot() *SyncProgress {
	j.mu.Lock()
	defer j.mu.Unlock()
	cp := j.stats
	return &cp
}

func (j *SyncJob) update(fn func(*SyncProgress)) {
	j.mu.Lock()
	fn(&j.stats)
	j.mu.Unlock()
}

// StartSyncJob 启动异步同步任务（同 URL 进行中则拒绝）。
// publish 用于 SSE 推送进度（可为 nil）。
func (m *Manager) StartSyncJob(repo SourceRepo, cleanup bool, publish func(task.Event)) (*SyncJob, error) {
	m.mu.Lock()
	if m.syncing[repo.URL] {
		m.mu.Unlock()
		return nil, fmt.Errorf("该仓库正在同步中")
	}
	m.syncing[repo.URL] = true
	m.mu.Unlock()
	// running 标记由 goroutine 结束时统一释放

	ctx, cancel := context.WithCancel(context.Background())
	job := &SyncJob{
		ID:       fmt.Sprintf("s%d", time.Now().UnixNano()),
		RepoName: repo.Name,
		URL:      repo.URL,
		Kind:     repo.Kind,
		Cleanup:  cleanup,
		running:  true,
		cancel:   cancel,
		stats:    SyncProgress{RepoName: repo.Name, Running: true},
	}

	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			delete(m.syncing, repo.URL)
			m.mu.Unlock()
		}()
		m.runSyncJob(ctx, job, repo, publish)
	}()
	return job, nil
}

// runSyncJob 执行同步：拉取 → 解析 → 分批入库（每批 200 条推送进度）。
func (m *Manager) runSyncJob(ctx context.Context, job *SyncJob, repo SourceRepo, publish func(task.Event)) {
	start := time.Now()
	push := func() {
		if publish == nil {
			return
		}
		snap := job.Snapshot()
		publish(task.Event{Type: "repo.sync.progress", Data: map[string]any{
			"jobId": job.ID, "repoName": snap.RepoName, "total": snap.Total,
			"done": snap.Done, "inserted": snap.Inserted, "updated": snap.Updated,
			"running": snap.Running, "err": snap.Err,
		}})
	}

	// 拉取
	text, status, err := m.fetchText(repo.URL, 120*time.Second)
	if err != nil {
		job.update(func(p *SyncProgress) { p.Err = err.Error(); p.Running = false; p.Finished = true })
		push()
		return
	}
	if status != 200 {
		job.update(func(p *SyncProgress) { p.Err = fmt.Sprintf("HTTP %d", status); p.Running = false; p.Finished = true })
		push()
		return
	}

	switch repo.Kind {
	case "replaceRule":
		// 净化规则量小，一次性导入
		res, ierr := m.ImportReplaceText(text)
		job.update(func(p *SyncProgress) {
			if ierr != nil {
				p.Err = ierr.Error()
			} else {
				p.Total, p.Inserted = res.Total, res.Inserted
				p.Done = res.Total
			}
			p.Running = false
			p.Finished = true
		})
	default:
		sources, perr := ParseSourceJSON([]byte(text))
		if perr != nil {
			job.update(func(p *SyncProgress) { p.Err = perr.Error(); p.Running = false; p.Finished = true })
			push()
			return
		}
		// 同仓清理：仓库不再提供的旧源。语义为"同步前清理"（与本仓库 origin 关联，
		// custom=0 的非手动导入源），先清后导——刚导入的新源不会被误删。
		if job.Cleanup {
			if n, cerr := m.st.DeleteSourcesByOrigin(repo.URL); cerr == nil && n > 0 {
				job.update(func(p *SyncProgress) { p.Removed = int(n) })
				push()
			}
		}
		job.update(func(p *SyncProgress) { p.Total = len(sources) })
		push()
		// 分批入库 + 取消检查
		const batch = 200
		for i := 0; i < len(sources); i += batch {
			if ctx.Err() != nil {
				job.update(func(p *SyncProgress) { p.Err = "已取消"; p.Running = false; p.Finished = true })
				push()
				return
			}
			end := i + batch
			if end > len(sources) {
				end = len(sources)
			}
			res := m.saveSources(sources[i:end], repo.URL, false)
			job.update(func(p *SyncProgress) {
				p.Done = end
				p.Inserted += res.Inserted
				p.Updated += res.Updated
			})
			push()
		}
	}
	job.mu.Lock()
	job.stats.ElapsedMS = time.Since(start).Milliseconds()
	job.stats.Running = false
	job.stats.Finished = true
	job.mu.Unlock()
	push()
}

// CancelSyncJob 取消同步任务。
func (j *SyncJob) Cancel() {
	j.mu.Lock()
	running := j.running
	j.mu.Unlock()
	if running && j.cancel != nil {
		j.cancel()
	}
}
