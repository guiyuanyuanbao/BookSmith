package export

import (
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"time"
)

// cover.go 封面图片下载（含防盗链处理：Referer 指向图片所在站点 + UA）。

// CoverImage 已下载的封面图。
type CoverImage struct {
	Data     []byte
	MimeType string
	Ext      string
}

var coverHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{},
	},
}

// DownloadCover 下载封面图片。失败返回 nil（不中断导出）。
// Referer 指向图片所在站点（绕过简单防盗链），支持重复协议头修正（https:https:// 或 https:http:// 等）。
func DownloadCover(rawURL string) *CoverImage {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	// 修正书源 JS 拼出的重复协议头：剥离外层 scheme，直到剩余部分以 scheme:// 开头
	// 例：https:https://a.com → https://a.com；https:http://a.com → http://a.com
	for i := 0; i < 4; i++ {
		idx := strings.Index(rawURL, ":")
		if idx <= 0 {
			break
		}
		head := strings.ToLower(rawURL[:idx])
		rest := rawURL[idx+1:]
		if head != "https" && head != "http" {
			break
		}
		lowRest := strings.ToLower(rest)
		if strings.HasPrefix(lowRest, "http://") || strings.HasPrefix(lowRest, "https://") {
			rawURL = rest
			continue
		}
		break
	}
	if !strings.HasPrefix(strings.ToLower(rawURL), "http://") && !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		return nil
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil
	}
	// Referer 指向图片站点根
	if idx := strings.Index(rawURL, "://"); idx > 0 {
		rest := rawURL[idx+3:]
		if slash := strings.Index(rest, "/"); slash > 0 {
			req.Header.Set("Referer", rawURL[:idx+3+slash+1])
		}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")

	resp, err := coverHTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(data) == 0 {
		return nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct == "" || ct == "application/octet-stream" || ct == "text/html" {
		ct = SniffImageMime(data)
	}
	if ct == "text/html" {
		return nil // HTML 错误页不是图片
	}
	ext := ".jpg"
	switch {
	case strings.Contains(ct, "png"):
		ext = ".png"
	case strings.Contains(ct, "gif"):
		ext = ".gif"
	case strings.Contains(ct, "webp"):
		ext = ".webp"
	}
	return &CoverImage{Data: data, MimeType: ct, Ext: ext}
}

// sniffImageMime 从字节头嗅探图片类型。
func SniffImageMime(d []byte) string {
	switch {
	case len(d) > 3 && d[0] == 0xFF && d[1] == 0xD8:
		return "image/jpeg"
	case len(d) > 8 && string(d[1:4]) == "PNG":
		return "image/png"
	case len(d) > 6 && string(d[0:3]) == "GIF":
		return "image/gif"
	case len(d) > 12 && string(d[8:12]) == "WEBP":
		return "image/webp"
	}
	return "text/html"
}
