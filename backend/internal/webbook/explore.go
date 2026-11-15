// explore.go 发现页：exploreUrl 分类解析 + 分类列表抓取解析 + 多源聚合。
package webbook

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"booksmith/internal/analyzer"
	"booksmith/internal/model"
)

// exploreKindsCache 分类解析缓存（exploreUrl 的 JS 求值可能较重，避免每次打开重复算）。
var exploreKindsCache = struct {
	mu    sync.Mutex
	items map[string]cachedExploreKinds
}{items: map[string]cachedExploreKinds{}}

type cachedExploreKinds struct {
	kinds     []model.ExploreKind
	fetchedAt time.Time
}

// ExploreKinds 解析源的分类列表（5 分钟缓存）。以下情形返回 nil（前端不展示）：
// 未启用发现、exploreUrl 为空、ruleExplore.bookList 为空（无法解析列表，点了也必然失败）。
func (s *Service) ExploreKinds(bs *model.BookSource) []model.ExploreKind {
	if bs == nil || !bs.EnabledExplore || strings.TrimSpace(bs.ExploreURL) == "" {
		return nil
	}
	if bs.RuleExplore == nil || strings.TrimSpace(bs.RuleExplore.BookList) == "" {
		return nil // 未配置发现规则：源树不展示，避免"点了就报错"的假可用源
	}
	key := bs.BookSourceURL
	exploreKindsCache.mu.Lock()
	if c, ok := exploreKindsCache.items[key]; ok && time.Since(c.fetchedAt) < 5*time.Minute {
		exploreKindsCache.mu.Unlock()
		return c.kinds
	}
	exploreKindsCache.mu.Unlock()

	kindsA := analyzer.AnalyzeExploreKinds(bs.ExploreURL, bs.BookSourceURL, bs, s.env.Engine(), s.env.Cookies())
	kinds := make([]model.ExploreKind, len(kindsA))
	for i, k := range kindsA {
		kinds[i] = model.ExploreKind{Title: k.Title, URL: k.URL}
	}
	exploreKindsCache.mu.Lock()
	exploreKindsCache.items[key] = cachedExploreKinds{kinds: kinds, fetchedAt: time.Now()}
	exploreKindsCache.mu.Unlock()
	return kinds
}

// ExploreOne 抓取单源某分类的书籍列表（page 从 1 开始）。
func (s *Service) ExploreOne(ctx context.Context, bs *model.BookSource, kindURL string, page int) ([]*model.ExploreBook, error) {
	if bs.RuleExplore == nil || strings.TrimSpace(bs.RuleExplore.BookList) == "" {
		return nil, fmt.Errorf("书源 %s 的 ruleExplore 未配置 bookList（该源无法解析发现列表）", bs.BookSourceName)
	}
	body, finalURL, err := s.fetchWithRule(ctx, bs, kindURL, bs.BookSourceURL, page, "")
	if err != nil {
		return nil, fmt.Errorf("发现请求失败: %w", err)
	}
	return s.analyzeExploreList(bs, body, finalURL)
}

// analyzeExploreList 解析发现列表（照 analyzeSearchList 模式逐字段提取）。
func (s *Service) analyzeExploreList(bs *model.BookSource, body, baseURL string) ([]*model.ExploreBook, error) {
	re := bs.RuleExplore
	ar := s.NewAnalyzeRule(bs, baseURL, nil)
	ar.SetContent(body, baseURL)
	ar.SetRedirectURL(baseURL)
	bookList := re.BookList
	if strings.HasPrefix(bookList, "-") || strings.HasPrefix(bookList, "+") {
		bookList = bookList[1:]
	}
	elements := ar.GetElements(bookList)
	out := make([]*model.ExploreBook, 0, len(elements))
	seen := map[string]bool{}
	for _, el := range elements {
		itemAR := s.NewAnalyzeRule(bs, baseURL, nil)
		itemAR.SetContent(el, baseURL)
		b := &model.ExploreBook{
			SourceURL:  bs.BookSourceURL,
			SourceName: bs.BookSourceName,
			Name:       formatBookName(itemAR.GetString(re.Name)),
			Author:     formatAuthor(itemAR.GetString(re.Author)),
			Kind:       strings.TrimSpace(itemAR.GetString(re.Kind)),
			Intro:      strings.TrimSpace(itemAR.GetString(re.Intro)),
			BookURL:    itemAR.GetStringURL(re.BookURL, nil),
			CoverURL:   itemAR.GetStringURL(re.CoverURL, nil),
		}
		if b.Name == "" || b.BookURL == "" {
			continue
		}
		if seen[b.BookURL] {
			continue
		}
		seen[b.BookURL] = true
		out = append(out, b)
	}
	return out, nil
}

// Explore 多源聚合发现：并发 8、每源 10s 超时、失败隔离（单源失败不影响整体）。
// sources 为参与聚合的源（调用方已按 EnabledExplore 过滤），kindURLs 与 sources 一一对应。
func (s *Service) Explore(ctx context.Context, sources []*model.BookSource, kindURLs []string, page int) ([]*model.ExploreBook, []string) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	out := make([]*model.ExploreBook, 0, len(sources)*20)
	errs := make([]string, 0)

	for i, bs := range sources {
		kindURL := ""
		if i < len(kindURLs) {
			kindURL = kindURLs[i]
		}
		if kindURL == "" {
			continue
		}
		wg.Add(1)
		go func(bs *model.BookSource, kindURL string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					errs = append(errs, fmt.Sprintf("%s: 规则异常 %v", bs.BookSourceName, r))
					mu.Unlock()
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			books, err := s.ExploreOne(cctx, bs, kindURL, page)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", bs.BookSourceName, err))
				return
			}
			start := time.Now()
			for _, b := range books {
				b.RespondTime = time.Since(start).Milliseconds()
				out = append(out, b)
			}
		}(bs, kindURL)
	}
	wg.Wait()
	return out, errs
}
