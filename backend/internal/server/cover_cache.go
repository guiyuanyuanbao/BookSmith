// cover_cache.go 封面磁盘缓存：data/covers/{sha256(url)[:32]}.img。
// 代理（/api/cover）与导出（DownloadCover 替代路径）共用，解决两类不确定性：
// 1) 远程站临时失败 → 下次直接命中磁盘缓存，不再"有的书有图有的没图"
// 2) 内存缓存超限清空导致集体失效
package server

import (
	"crypto/sha256"
	"fmt"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)
// coverCachePath URL → 磁盘缓存文件路径。
func coverCachePath(dataDir, rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(dataDir, "covers", hex.EncodeToString(sum[:])[:32]+".img")
}

// coverCacheFileKey URL → 磁盘缓存文件名（LRU 索引键）。
func coverCacheFileKey(dataDir, rawURL string) string {
	return filepath.Base(coverCachePath(dataDir, rawURL))
}

// coverCacheGet 读磁盘缓存；命中返回 (data, mime, true)。
func coverCacheGet(dataDir, rawURL string) ([]byte, string, bool) {
	p := coverCachePath(dataDir, rawURL)
	data, err := os.ReadFile(p)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	// mime 存于文件头行：第一行 "mime:<type>\n" + 图片字节
	idx := 0
	for i, b := range data {
		if b == '\n' {
			idx = i
			break
		}
	}
	if idx == 0 || idx > 128 || !strings.HasPrefix(string(data[:idx]), "mime:") {
		return nil, "", false
	}
	mime := strings.TrimPrefix(string(data[:idx]), "mime:")
	return data[idx+1:], mime, true
}

// coverTmpSeq tmp 文件名原子序号：同进程并发写同一 URL 时避免共享路径交叉写坏缓存。
var coverTmpSeq atomic.Int64

// coverCachePut 写入磁盘缓存（图片字节 + mime 头）。
func coverCachePut(dataDir, rawURL string, data []byte, mime string) {
	os.MkdirAll(filepath.Join(dataDir, "covers"), 0o755)
	p := coverCachePath(dataDir, rawURL)
	tmp := fmt.Sprintf("%s.%d.%d.tmp", p, os.Getpid(), coverTmpSeq.Add(1))
	if err := os.WriteFile(tmp, append([]byte("mime:"+mime+"\n"), data...), 0o644); err != nil {
		return
	}
	os.Rename(tmp, p) // 原子落盘
}

// fetchAndCacheCover 抓取远程封面并写缓存。返回 (data, mime, ok)。
// 走全局信号量与缓存查询（未进 in-flight 去重——调用方为逐源探测，URL 各异；
// 同 URL 并发由 coverCachePut 的唯一 tmp 名兜底）。
func (s *Server) fetchAndCacheCover(rawURL string) ([]byte, string, bool) {
	// 先查缓存（其他探测协程可能已写入同一封面）
	if d, m, ok := s.app.coverCacheLookup(rawURL); ok {
		return d, m, true
	}
	if s.app.coverFetchSem != nil {
		s.app.coverFetchSem <- struct{}{}
		defer func() { <-s.app.coverFetchSem }()
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false
	}
	if u := parseURLHost(rawURL); u != "" {
		req.Header.Set("Referer", u)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	resp, err := s.app.CoverClient.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" || ct == "text/html" {
		ct = sniffImageType(data)
	}
	if !strings.HasPrefix(ct, "image/") {
		return nil, "", false
	}
	coverCachePut(s.app.DataDir, rawURL, data, ct)
	s.app.coverCacheStore(rawURL, data, ct)
	return data, ct, true
}

// CachedCoverImage 对外提供：导出层获取封面（内存/磁盘 LRU 优先，未命中远程抓取并缓存）。
// 与代理同走信号量 + in-flight 去重（导出多格式并发取同一封面时共享一次抓取）。
// 由 main 装配到 taskMgr.CoverProvider。
func (a *App) CachedCoverImage(rawURL string) ([]byte, string, bool) {
	if rawURL == "" || !strings.HasPrefix(rawURL, "http") {
		return nil, "", false
	}
	return a.fetchCoverDeduped(rawURL)
}

// fetchAndCacheCoverDirect 直接抓取（调用方负责信号量；导出路径经 fetchCoverDeduped 已持有信号量时使用）。
func (a *App) fetchAndCacheCoverDirect(rawURL string) ([]byte, string, bool) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false
	}
	if u := parseURLHost(rawURL); u != "" {
		req.Header.Set("Referer", u)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	resp, err := a.CoverClient.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" || ct == "text/html" {
		ct = sniffImageType(data)
	}
	if !strings.HasPrefix(ct, "image/") {
		return nil, "", false
	}
	a.coverCacheStore(rawURL, data, ct)
	return data, ct, true
}

// evictCovers 按封面 URL 回收磁盘缓存文件（删除书籍时调用）。
// 仅删除无其他书籍引用的 URL；LRU 索引与内存热缓存同步反注册。
func (s *Server) evictCovers(urls []string) {
	if len(urls) == 0 {
		return
	}
	// 全库仍在使用的封面 URL 集合（分页遍历：藏书超 1 万时 ListBooks 单页会漏判误删）
	inUse := map[string]bool{}
	const pageSize = 5000
	for offset := 0; ; offset += pageSize {
		all, _, err := s.app.Store.ListBooks("", pageSize, offset)
		if err != nil || len(all) == 0 {
			break
		}
		for _, b := range all {
			if b.CoverURL != "" {
				inUse[b.CoverURL] = true
			}
		}
		if len(all) < pageSize {
			break
		}
	}
	// 当前被删书籍已不在库里（或即将删除），逐 URL 判断
	for _, u := range urls {
		if u == "" || inUse[u] {
			continue
		}
		if s.app.CoverDiskLRU != nil {
			key := coverCacheFileKey(s.app.DataDir, u)
			_ = os.Remove(filepath.Join(s.app.DataDir, "covers", key))
			s.app.CoverDiskLRU.Remove(key)
		}
		s.app.CoverMemLRU.Remove(u)
	}
}
