// probe.go 目录探针：单源拉取目录的质量预览（章节数/首末章/卷数）。
// 结果缓存（bookUrl+sourceUrl → 10 分钟），供比源对话框按需探测与换源引导共用。
package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"booksmith/internal/model"
)

// TocProbeResult 目录探测结果。
type TocProbeResult struct {
	OK            bool     `json:"ok"`
	Err           string   `json:"err,omitempty"`
	Total         int      `json:"total"`                  // 正文章节数（不含卷）
	VolumeCount   int      `json:"volumeCount,omitempty"`  // 卷数
	FirstChapters []string `json:"firstChapters"`          // 前 5 章标题
	LastChapters  []string `json:"lastChapters"`           // 末 3 章标题
	ProbeMs       int64    `json:"probeMs"`
	Cached        bool     `json:"cached,omitempty"`
}

// probeCacheEntry 探测缓存条目。
type probeCacheEntry struct {
	result  TocProbeResult
	expires time.Time
}

// probeTTL 探测结果缓存时长。
const probeTTL = 10 * time.Minute

var (
	probeMu    sync.Mutex
	probeCache = map[string]probeCacheEntry{}
)

// probeCacheKey 探测缓存键。
func probeCacheKey(sourceURL, bookURL string) string {
	return sourceURL + "|" + bookURL
}

// probeCacheGet 读缓存（未过期）。
func probeCacheGet(key string) (TocProbeResult, bool) {
	probeMu.Lock()
	defer probeMu.Unlock()
	e, ok := probeCache[key]
	if !ok || time.Now().After(e.expires) {
		return TocProbeResult{}, false
	}
	r := e.result
	r.Cached = true
	return r, true
}

// probeCachePut 写缓存；超 64 条整体清空（探测是低频操作，防膨胀足够）。
func probeCachePut(key string, r TocProbeResult) {
	probeMu.Lock()
	if len(probeCache) > 64 {
		probeCache = map[string]probeCacheEntry{}
	}
	probeCache[key] = probeCacheEntry{result: r, expires: time.Now().Add(probeTTL)}
	probeMu.Unlock()
}

// probeTocResult 探测单源目录质量（单源与批量处理器共用的核心逻辑）。
// 对齐真实下载路径（fetchToc）：先经详情页解析 ruleBookInfo.tocUrl 得到真实
// 目录地址（大量源目录与详情页不同页），失败回退 bookUrl——否则探针会把
// 好源误判成「目录解析为空」。
func (s *Server) probeTocResult(ctx context.Context, bs *model.BookSource, bookURL string) TocProbeResult {
	start := time.Now()
	book := &model.Book{BookURL: bookURL, TocURL: bookURL}
	if info, err := s.app.WebBook.GetBookInfo(ctx, bs, bookURL); err == nil {
		if info.TocURL != "" {
			book.TocURL = info.TocURL
		}
	} else if err != nil && bs.RuleBookInfo != nil && strings.TrimSpace(bs.RuleBookInfo.TocURL) != "" {
		// 完整详情解析失败（如书名规则未配导致「详情解析书名为空」）但 tocUrl
		// 规则存在：最小化提取目录地址——与真实下载行为对齐的最后一道兜底
		if toc, terr := s.app.WebBook.ResolveTocURL(ctx, bs, bookURL); terr == nil && toc != "" {
			book.TocURL = toc
		}
	}
	chapters, tocErr := s.app.WebBook.GetToc(ctx, bs, book)
	result := TocProbeResult{
		ProbeMs:       time.Since(start).Milliseconds(),
		FirstChapters: []string{}, // 初始化非 nil：nil 切片序列化为 null，前端 .length 崩溃（弹窗消失 bug）
		LastChapters:  []string{},
	}

	if tocErr != nil {
		result.Err = tocErr.Error()
		return result
	}
	result.OK = true
	for _, ch := range chapters {
		if ch.IsVolume {
			result.VolumeCount++
			continue
		}
		result.Total++
		if len(result.FirstChapters) < 5 {
			result.FirstChapters = append(result.FirstChapters, ch.Title)
		}
	}
	if result.Total > 3 {
		for i := len(chapters) - 1; i >= 0 && len(result.LastChapters) < 3; i-- {
			if !chapters[i].IsVolume {
				result.LastChapters = append([]string{chapters[i].Title}, result.LastChapters...)
			}
		}
	}
	return result
}

// handleProbeToc POST /api/probe-toc —— body: {sourceUrl, bookUrl}。
// 单源拉取目录并返回质量摘要；结果缓存供比源对话框与换源引导共用。
func (s *Server) handleProbeToc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceURL string `json:"sourceUrl"`
		BookURL   string `json:"bookUrl"`
	}
	if err := readJSON(r, &req); err != nil || req.SourceURL == "" || req.BookURL == "" {
		writeErr(w, 400, "需要 sourceUrl 与 bookUrl")
		return
	}

	key := probeCacheKey(req.SourceURL, req.BookURL)
	if cached, ok := probeCacheGet(key); ok {
		writeOK(w, cached)
		return
	}

	bsAny, err := s.app.Store.GetSource(req.SourceURL)
	if err != nil {
		writeErr(w, 404, "书源不存在")
		return
	}
	bs := bsAny

	result := s.probeTocResult(r.Context(), bs, req.BookURL)
	if result.OK {
		probeCachePut(key, result)
	} else {
		// 失败结果缓存更短（2 分钟），源站恢复后可重探
		probeMu.Lock()
		probeCache[key] = probeCacheEntry{result: result, expires: time.Now().Add(2 * time.Minute)}
		probeMu.Unlock()
	}
	writeOK(w, result)
}

// handleProbeBatch POST /api/probe-toc/batch —— body: {sources: [{sourceUrl, bookUrl}]}。
// 并发探测多个源（上限 6 并发），全部返回后按响应时间排序。
// 探测逻辑与单源一致（probeTocResult：先解析 ruleBookInfo.tocUrl 再取目录）。
func (s *Server) handleProbeTocBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sources []struct {
			SourceURL string `json:"sourceUrl"`
			BookURL   string `json:"bookUrl"`
		} `json:"sources"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Sources) == 0 {
		writeErr(w, 400, "需要 sources 数组")
		return
	}
	if len(req.Sources) > 20 {
		req.Sources = req.Sources[:20]
	}

	type item struct {
		SourceURL string         `json:"sourceUrl"`
		BookURL   string         `json:"bookUrl"`
		Result    TocProbeResult `json:"result"`
	}
	out := make([]item, len(req.Sources))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, src := range req.Sources {
		wg.Add(1)
		go func(i int, src struct {
			SourceURL string `json:"sourceUrl"`
			BookURL   string `json:"bookUrl"`
		}) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i].SourceURL = src.SourceURL
			out[i].BookURL = src.BookURL
			key := probeCacheKey(src.SourceURL, src.BookURL)
			if cached, ok := probeCacheGet(key); ok {
				out[i].Result = cached
				return
			}
			bsAny, err := s.app.Store.GetSource(src.SourceURL)
			if err != nil {
				out[i].Result = TocProbeResult{Err: "书源不存在"}
				return
			}
			result := s.probeTocResult(r.Context(), bsAny, src.BookURL)
			if result.OK {
				probeCachePut(key, result)
			} else {
				// 失败结果缓存更短（2 分钟），源站恢复后可重探
				probeMu.Lock()
				probeCache[key] = probeCacheEntry{result: result, expires: time.Now().Add(2 * time.Minute)}
				probeMu.Unlock()
			}
			out[i].Result = result
		}(i, src)
	}
	wg.Wait()
	// 稳定排序：OK 的在前，探测快的在前
	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := out[a].Result, out[b].Result
		if ra.OK != rb.OK {
			return ra.OK
		}
		return ra.ProbeMs < rb.ProbeMs
	})
	writeOK(w, out)
}
