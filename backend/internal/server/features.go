package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"booksmith/internal/model"
	"booksmith/internal/replace"
	"booksmith/internal/task"
	"booksmith/internal/webbook"
)

// ---------------- 搜索任务（异步 + 进度 + 取消） ----------------

// SearchJob 一次聚合搜索任务。
type SearchJob struct {
	ID        string
	Key       string
	mu        sync.Mutex
	agg       *webbook.Aggregated
	running   bool
	cancel    context.CancelFunc
	StartedAt time.Time
}

// Snapshot 任务快照。
func (j *SearchJob) Snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	items := j.agg.Items
	if items == nil {
		items = []*model.SearchResult{}
	}
	errs := j.agg.Errors
	if errs == nil {
		errs = []webbook.SourceError{}
	}
	elapsed := j.agg.ElapsedMS
	if j.running {
		elapsed = time.Since(j.StartedAt).Milliseconds()
	}
	return map[string]any{
		"searchId":  j.ID,
		"key":       j.Key,
		"running":   j.running,
		"searched":  j.agg.Searched,
		"total":     j.agg.Total,
		"elapsedMs": elapsed,
		"items":     items,
		"errors":    errs,
	}
}

func (j *SearchJob) setProgress(agg *webbook.Aggregated) {
	j.mu.Lock()
	j.agg = agg
	j.mu.Unlock()
}

func (j *SearchJob) finish(agg *webbook.Aggregated) {
	j.mu.Lock()
	j.agg = agg
	j.running = false
	j.mu.Unlock()
}

// startSearch 启动搜索任务（同 key 进行中则复用，legado 语义）。
func (s *Server) startSearch(key string, sourceURLs []string) (*SearchJob, error) {
	s.app.SearchMu.Lock()
	defer s.app.SearchMu.Unlock()

	// 复用进行中的同 key 搜索
	for _, j := range s.app.SearchJobs {
		j.mu.Lock()
		running, k := j.running, j.Key
		j.mu.Unlock()
		if running && k == key {
			return j, nil
		}
	}

	sources, err := s.app.FindEnabledSources()
	if err != nil {
		return nil, err
	}
	if len(sourceURLs) > 0 {
		allow := map[string]bool{}
		for _, u := range sourceURLs {
			allow[u] = true
		}
		filtered := make([]*model.BookSource, 0, len(sources))
		for _, bs := range sources {
			if allow[bs.BookSourceURL] {
				filtered = append(filtered, bs)
			}
		}
		sources = filtered
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("没有可用的启用了搜索的书源（请先导入并启用书源）")
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &SearchJob{
		ID:        fmt.Sprintf("s%d", time.Now().UnixNano()),
		Key:       key,
		running:   true,
		cancel:    cancel,
		StartedAt: time.Now(),
		agg:       &webbook.Aggregated{Total: len(sources)},
	}
	s.app.SearchJobs[job.ID] = job
	// 仅保留最近 8 个任务
	for len(s.app.SearchJobs) > 8 {
		var oldest string
		var oldestT time.Time
		for id, j := range s.app.SearchJobs {
			if oldest == "" || j.StartedAt.Before(oldestT) {
				oldest, oldestT = id, j.StartedAt
			}
		}
		if j := s.app.SearchJobs[oldest]; j != nil && !j.running {
			delete(s.app.SearchJobs, oldest)
		} else {
			break
		}
	}

	go func() {
		defer cancel()
		agg := s.app.WebBook.Search(ctx, sources, key, func(snap *webbook.Aggregated) {
			job.setProgress(snap)
			s.app.Bus.Publish(task.Event{Type: "search.progress", Data: map[string]any{
				"searchId": job.ID, "key": key, "searched": snap.Searched, "total": snap.Total,
				"items": len(snap.Items),
			}})
		})
		if ctx.Err() != nil {
			agg.ElapsedMS = time.Since(job.StartedAt).Milliseconds()
		}
		job.finish(agg)
		s.app.Bus.Publish(task.Event{Type: "search.done", Data: map[string]any{
			"searchId": job.ID, "key": key, "items": len(agg.Items),
		}})
	}()
	return job, nil
}

func (s *Server) handleSearchStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key        string   `json:"key"`
		SourceURLs []string `json:"sourceUrls"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Key == "" {
		writeErr(w, 400, "key 不能为空")
		return
	}
	job, err := s.startSearch(req.Key, req.SourceURLs)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeOK(w, map[string]any{"searchId": job.ID, "running": true, "key": job.Key})
}

func (s *Server) handleSearchGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.app.SearchMu.Lock()
	job := s.app.SearchJobs[id]
	s.app.SearchMu.Unlock()
	if job == nil {
		writeErr(w, 404, "搜索任务不存在或已过期")
		return
	}
	writeOK(w, job.Snapshot())
}

func (s *Server) handleSearchCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.app.SearchMu.Lock()
	job := s.app.SearchJobs[id]
	s.app.SearchMu.Unlock()
	if job == nil {
		writeErr(w, 404, "搜索任务不存在")
		return
	}
	job.cancel()
	writeOK(w, nil)
}

// ---------------- 封面代理（防盗链 + 缓存） ----------------

type coverCacheEntry struct {
	data     []byte
	httpType string
}

// StartCoverProxy 初始化封面代理缓存与客户端。
// 内存热缓存 256 条 LRU；磁盘 covers 目录建索引（5000 个/500MB 上限，超限驱逐最冷文件）。
func (s *App) StartCoverProxy() {
	s.CoverClient = &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{},
		},
	}
	coversDir := filepath.Join(s.DataDir, "covers")
	diskLRU := NewCoverLRU(coverMaxEntries, coverMaxBytes, func(key string) {
		_ = os.Remove(filepath.Join(coversDir, key)) // 驱逐即删文件
	})
	scanCoversDir(coversDir, diskLRU)
	s.CoverDiskLRU = diskLRU
	s.CoverMemLRU = NewCoverLRU(memCoverMax, 0, nil) // 内存层仅条目数上限
}

// coverCacheLookup 三层读取：内存 LRU → 磁盘（+LRU 索引提温）→ 未命中。
func (s *App) coverCacheLookup(rawURL string) ([]byte, string, bool) {
	if e, ok := s.CoverMemLRU.memGet(rawURL); ok {
		return e.data, e.httpType, true
	}
	if d, m, ok := coverCacheGet(s.DataDir, rawURL); ok {
		if s.CoverDiskLRU != nil {
			s.CoverDiskLRU.Touch(coverCacheFileKey(s.DataDir, rawURL), int64(len(d)))
		}
		s.CoverMemLRU.memPut(rawURL, coverCacheEntry{data: d, httpType: m})
		return d, m, true
	}
	return nil, "", false
}

// coverCacheStore 两级写入：磁盘文件 + LRU 索引 + 内存热缓存。
func (s *App) coverCacheStore(rawURL string, data []byte, mime string) {
	coverCachePut(s.DataDir, rawURL, data, mime)
	if s.CoverDiskLRU != nil {
		s.CoverDiskLRU.Touch(coverCacheFileKey(s.DataDir, rawURL), int64(len(data)))
	}
	s.CoverMemLRU.memPut(rawURL, coverCacheEntry{data: data, httpType: mime})
}

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		writeErr(w, 400, "需要 url 参数")
		return
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		writeErr(w, 400, "非法封面 URL")
		return
	}
	// 第1+2层：内存 LRU → 磁盘 LRU（跨重启持久，解决远程站临时失败导致的"时有无"）
	if d, m, ok := s.app.coverCacheLookup(rawURL); ok {
		w.Header().Set("Content-Type", m)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(d)
		return
	}
	// 第3层：远程抓取并写两级缓存
	data, ct, ok := s.fetchAndCacheCover(rawURL)
	if !ok {
		writeErr(w, 502, "封面获取失败（源站暂时不可用，稍后重试或换源）")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

func parseURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/"
}

func sniffImageType(d []byte) string {
	switch {
	case len(d) > 3 && d[0] == 0xFF && d[1] == 0xD8:
		return "image/jpeg"
	case len(d) > 8 && string(d[1:4]) == "PNG":
		return "image/png"
	case len(d) > 6 && (string(d[0:3]) == "GIF"):
		return "image/gif"
	case len(d) > 12 && string(d[8:12]) == "WEBP":
		return "image/webp"
	}
	return "image/jpeg"
}

// ---------------- 书源校验 ----------------

func (s *Server) handleVerifySources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URLs []string `json:"urls"`
	}
	if err := readJSON(r, &req); err != nil || len(req.URLs) == 0 {
		writeErr(w, 400, "需要 urls 数组")
		return
	}
	type vResult struct {
		URL       string `json:"url"`
		Name      string `json:"name"`
		OK        bool   `json:"ok"`
		ElapsedMS int64  `json:"elapsedMs"`
		Err       string `json:"err,omitempty"`
	}
	out := make([]vResult, 0, len(req.URLs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	for _, u := range req.URLs {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			bsAny, err := s.app.Store.GetSource(u)
			if err != nil {
				mu.Lock()
				out = append(out, vResult{URL: u, OK: false, Err: "书源不存在"})
				mu.Unlock()
				return
			}
			bs := bsAny
			start := time.Now()
			keyword := "我的"
			if bs.RuleSearch != nil && bs.RuleSearch.CheckKeyWord != "" &&
				!strings.Contains(bs.RuleSearch.CheckKeyWord, "http") {
				keyword = strings.TrimSpace(bs.RuleSearch.CheckKeyWord)
			}
			_, serr := s.app.WebBook.SearchOne(ctx, bs, keyword, 1)
			elapsed := time.Since(start).Milliseconds()
			vr := vResult{URL: u, Name: bs.BookSourceName, ElapsedMS: elapsed}
			if serr != nil {
				vr.Err = serr.Error()
			} else {
				vr.OK = true
			}
			_ = s.app.Store.SetSourceVerified(u, vr.OK, vr.Err, elapsed)
			mu.Lock()
			out = append(out, vr)
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	writeOK(w, out)
}

// ---------------- 净化规则测试 ----------------

func (s *Server) handleTestReplace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pattern     string `json:"pattern"`
		Replacement string `json:"replacement"`
		IsRegex     bool   `json:"isRegex"`
		Text        string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Pattern == "" || req.Text == "" {
		writeErr(w, 400, "pattern 与 text 不能为空")
		return
	}
	rule := &model.ReplaceRule{
		Name: "测试", Pattern: req.Pattern, Replacement: req.Replacement,
		IsRegex: req.IsRegex, Enabled: true, ScopeContent: true,
	}
	p := newSingleRuleProcessor(rule)
	writeOK(w, map[string]any{"result": p.ApplyContent(req.Text, "", "")})
}

// ---------------- 书源调试 ----------------

func (s *Server) handleSourceDebug(w http.ResponseWriter, r *http.Request) {
	srcURL := r.URL.Query().Get("url")
	key := r.URL.Query().Get("key")
	if key == "" {
		key = "斗破苍穹"
	}
	bs, err := s.app.Store.GetSource(srcURL)
	if err != nil {
		writeErr(w, 404, "书源不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	trace := s.app.WebBook.DebugSearch(ctx, bs, key)
	writeOK(w, trace)
}

// ---------------- 任务失败章节 ----------------

func (s *Server) handleTaskFailures(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	t, err := s.app.TaskMgr.GetTask(id)
	if err != nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	failures, err := s.app.Store.FailedChapters(t.BookID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if failures == nil {
		failures = []*model.Chapter{}
	}
	writeOK(w, failures)
}

// newSingleRuleProcessor 单条净化规则处理器。
func newSingleRuleProcessor(rule *model.ReplaceRule) *replace.Processor {
	return replace.NewProcessor([]*model.ReplaceRule{rule}, nil, nil)
}
