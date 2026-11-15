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
// 未启用发现、exploreUrl 为空、发现与搜索规则均无 bookList（无法解析任何列表）。
// ruleExplore.bookList 为空但搜索规则可用 → 仍展示（legado 回退语义，ExploreOne 会用搜索规则）。
func (s *Service) ExploreKinds(bs *model.BookSource) []model.ExploreKind {
	if bs == nil || !bs.EnabledExplore || strings.TrimSpace(bs.ExploreURL) == "" {
		return nil
	}
	if (bs.RuleExplore == nil || strings.TrimSpace(bs.RuleExplore.BookList) == "") &&
		(bs.RuleSearch == nil || strings.TrimSpace(bs.RuleSearch.BookList) == "") {
		return nil // 发现与搜索规则均缺失：点了也必然失败
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
// 语义对齐 legado BookList.analyzeBookList：ruleExplore.bookList 为空时
// **回退用 searchRule** 解析（legado 原版行为，大量源不配 ruleExplore 也能发现）。
func (s *Service) ExploreOne(ctx context.Context, bs *model.BookSource, kindURL string, page int) ([]*model.ExploreBook, error) {
	// legado BookList.kt:84-88：explore bookList 空白 → 用搜索规则
	useSearchRule := bs.RuleExplore == nil || strings.TrimSpace(bs.RuleExplore.BookList) == ""
	body, finalURL, err := s.fetchWithRule(ctx, bs, kindURL, bs.BookSourceURL, page, "")
	if err != nil {
		return nil, fmt.Errorf("发现请求失败: %w", err)
	}
	return s.analyzeExploreList(bs, body, finalURL, useSearchRule)
}

// analyzeExploreList 解析发现列表（照 analyzeSearchList 模式逐字段提取）。
// useSearchRule=true 时用搜索规则解析（legado 回退语义）。
func (s *Service) analyzeExploreList(bs *model.BookSource, body, baseURL string, useSearchRule bool) ([]*model.ExploreBook, error) {
	re := bs.RuleExplore
	if useSearchRule {
		re = bs.RuleSearch
	}
	if re == nil || strings.TrimSpace(re.BookList) == "" {
		return nil, fmt.Errorf("书源 %s 的发现与搜索规则均未配置 bookList（无法解析列表）", bs.BookSourceName)
	}
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
