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
)

// coverCachePath URL → 磁盘缓存文件路径。
func coverCachePath(dataDir, rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(dataDir, "covers", hex.EncodeToString(sum[:])[:32]+".img")
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

// coverCachePut 写入磁盘缓存（图片字节 + mime 头）。
func coverCachePut(dataDir, rawURL string, data []byte, mime string) {
	os.MkdirAll(filepath.Join(dataDir, "covers"), 0o755)
	p := coverCachePath(dataDir, rawURL)
	tmp := fmt.Sprintf("%s.%d.tmp", p, os.Getpid())
	if err := os.WriteFile(tmp, append([]byte("mime:"+mime+"\n"), data...), 0o644); err != nil {
		return
	}
	os.Rename(tmp, p) // 原子落盘
}

// fetchAndCacheCover 抓取远程封面并写缓存。返回 (data, mime, ok)。
func (s *Server) fetchAndCacheCover(rawURL string) ([]byte, string, bool) {
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
	return data, ct, true
}

// CachedCoverImage 对外提供：导出层获取封面（磁盘缓存优先，未命中远程抓取并缓存）。
// 由 main 装配到 taskMgr.CoverProvider。
// CachedCoverImage 对外提供（App 级，供 main 装配）。
func (a *App) CachedCoverImage(rawURL string) ([]byte, string, bool) {
	if rawURL == "" || !strings.HasPrefix(rawURL, "http") {
		return nil, "", false
	}
	if d, m, ok := coverCacheGet(a.DataDir, rawURL); ok {
		return d, m, true
	}
	// 远程抓取并写缓存（复用代理同款客户端与逻辑）
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false
	}
	if u := parseURLHost(rawURL); u != "" {
		req.Header.Set("Referer", u)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
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
	coverCachePut(a.DataDir, rawURL, data, ct)
	return data, ct, true
}
