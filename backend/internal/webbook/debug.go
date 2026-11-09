package webbook

import (
	"context"
	"fmt"
	"strings"
	"time"

	"booksmith/internal/model"
)

// debug.go 书源调试：逐步骤 trace（对标 legado 书源调试）。

// DebugStep 调试步骤。
type DebugStep struct {
	Name      string `json:"name"`
	Detail    string `json:"detail,omitempty"`
	OK        bool   `json:"ok"`
	ElapsedMS int64  `json:"elapsedMs"`
}

// DebugTrace 完整调试轨迹。
type DebugTrace struct {
	Steps []DebugStep           `json:"steps"`
	Items []*model.SearchOrigin `json:"items,omitempty"`
	Err   string                `json:"err,omitempty"`
}

// DebugSearch 对书源执行一次带 trace 的搜索（可选深入目录与正文）。
func (s *Service) DebugSearch(ctx context.Context, bs *model.BookSource, key string) *DebugTrace {
	trace := &DebugTrace{Steps: []DebugStep{}}
	step := func(name, detail string, ok bool, start time.Time) {
		trace.Steps = append(trace.Steps, DebugStep{
			Name: name, Detail: detail, OK: ok, ElapsedMS: time.Since(start).Milliseconds(),
		})
	}

	if strings.TrimSpace(bs.SearchURL) == "" {
		trace.Err = "书源未配置搜索 URL"
		step("解析搜索 URL", "searchUrl 为空", false, time.Now())
		return trace
	}

	// 1. 请求搜索页
	st := time.Now()
	body, finalURL, err := s.fetchWithRule(ctx, bs, bs.SearchURL, bs.BookSourceURL, 1, key)
	if err != nil {
		step("请求搜索页", err.Error(), false, st)
		trace.Err = err.Error()
		return trace
	}
	step("请求搜索页", fmt.Sprintf("%s · %d 字节", finalURL, len(body)), true, st)

	// 2. 解析列表
	st = time.Now()
	items, err := s.analyzeSearchList(bs, body, finalURL, key)
	if err != nil {
		step("解析搜索结果", err.Error(), false, st)
		trace.Err = err.Error()
		return trace
	}
	step("解析搜索结果", fmt.Sprintf("命中 %d 条", len(items)), true, st)
	trace.Items = items
	if len(items) == 0 {
		return trace
	}

	// 3. 字段抽取样例
	first := items[0]
	step("字段抽取", fmt.Sprintf("书名=%q 作者=%q 分类=%q 最新=%q 书籍URL=%s",
		trunc(first.Name, 24), trunc(first.Author, 16), trunc(first.Kind, 12),
		trunc(first.LatestChapter, 24), trunc(first.BookURL, 60)), true, time.Now())

	// 4. 详情与目录（抓取书籍详情页后解析）
	st = time.Now()
	detailBody, detailURL, err := s.fetchWithRule(ctx, bs, first.BookURL, finalURL, 1, "")
	if err != nil {
		step("抓取详情页", err.Error(), false, st)
		trace.Err = err.Error()
		return trace
	}
	book, err := s.analyzeBookInfo(bs, detailBody, detailURL)
	if err != nil {
		step("解析详情", err.Error(), false, st)
		trace.Err = err.Error()
		return trace
	}
	book.BookURL = first.BookURL
	step("解析详情", fmt.Sprintf("书名=%q 作者=%q", trunc(book.Name, 24), trunc(book.Author, 16)), true, st)

	st = time.Now()
	bookCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	chapters, err := s.GetToc(bookCtx, bs, book)
	if err != nil {
		step("解析目录", err.Error(), false, st)
		trace.Err = err.Error()
		return trace
	}
	vol, normal := 0, 0
	for _, ch := range chapters {
		if ch.IsVolume {
			vol++
		} else {
			normal++
		}
	}
	step("解析目录", fmt.Sprintf("共 %d 项（卷 %d / 章 %d）", len(chapters), vol, normal), true, st)
	if normal == 0 {
		return trace
	}

	// 5. 正文抽样
	var firstChapter *model.Chapter
	for _, ch := range chapters {
		if !ch.IsVolume {
			firstChapter = ch
			break
		}
	}
	st = time.Now()
	cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
	defer ccancel()
	content, err := s.GetContent(cctx, bs, book, firstChapter)
	if err != nil {
		step("解析正文", err.Error(), false, st)
		trace.Err = err.Error()
		return trace
	}
	sample := content
	if r := []rune(sample); len(r) > 80 {
		sample = string(r[:80]) + "…"
	}
	step("解析正文", fmt.Sprintf("%d 字 · 样例: %s", len([]rune(content)), sample), true, st)
	return trace
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
