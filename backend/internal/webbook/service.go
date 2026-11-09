// Package webbook 实现书籍获取编排：单源/聚合搜索、详情、目录（翻页/去重）、正文（翻页/净化）。
// 语义对齐 legado WebBook/BookList/BookInfo/BookChapterList/BookContent。
package webbook

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"booksmith/internal/analyzer"
	"booksmith/internal/content"
	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/replace"
	"booksmith/internal/util"
	"booksmith/internal/varstore"
)

// Service 编排服务。
type Service struct {
	client *httpx.Client
	env    *jsenv.Env
	// ReplaceProvider 提供净化规则（由上层注入，避免依赖 store）
	ReplaceProvider func() []*model.ReplaceRule
	// SourceConcurrency 聚合搜索并发源数
	SourceConcurrency int
	// PageConcurrency 目录/正文分页并发数
	PageConcurrency int
	// NextChapterProvider 查询下一章 URL（task 层注入，返回 "" 表示未知）
	NextChapterProvider func(book *model.Book, index int) string
}

// New 创建服务。
func New(client *httpx.Client, env *jsenv.Env) *Service {
	return &Service{
		client:            client,
		env:               env,
		SourceConcurrency: 96,
		PageConcurrency:   4,
	}
}

// NewAnalyzeRule 创建绑定好上下文的规则分析器。
func (s *Service) NewAnalyzeRule(bs *model.BookSource, baseURL string, vars varstore.VariableProvider) *analyzer.AnalyzeRule {
	if vars == nil {
		vars = varstore.New(nil)
	}
	ar := analyzer.NewAnalyzeRule(bs, s.env.Engine(), vars)
	ar.SetBaseURL(baseURL)
	ext := s.env.NewExtensions(bs, vars, baseURL)
	ar.Bind("java", ext)
	ar.Bind("source", jsenv.NewJSSource(bs, ext))
	return ar
}

// applyRateLimit 应用书源并发率。
func (s *Service) applyRateLimit(bs *model.BookSource) {
	if bs == nil {
		return
	}
	s.env.ApplySourceRateLimit(bs.BookSourceURL, bs.ConcurrentRate)
}

// fetch 执行 AnalyzeUrl 请求并返回解码文本与最终 URL。
func (s *Service) fetch(ctx context.Context, au *analyzer.AnalyzeUrl) (string, string, error) {
	req := au.BuildRequest(nil)
	resp, err := s.client.Do(ctx, req)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, req.URL)
	}
	return resp.BodyText(), resp.URL, nil
}

// 书名/作者清洗（legado BookHelp.formatBookName/formatBookAuthor）。
var (
	bookNameSuffixRe = regexp.MustCompile(`\s+作\s*者.*|\s+\S+\s+著`)
	authorPrefixRe   = regexp.MustCompile(`^\s*作\s*者[:：\s]+|\s+著`)
	// 部分源书名规则直接取到带高亮标签的 HTML（如 <em class="keyword">xxx</em>）
	htmlTagRe      = regexp.MustCompile(`(?s)<[^>]*>`)
	htmlEntityAmpRe = regexp.MustCompile(`&amp;`)
)

func formatBookName(name string) string {
	name = strings.TrimSpace(name)
	if strings.Contains(name, "<") && strings.Contains(name, ">") {
		name = htmlTagRe.ReplaceAllString(name, "")
		name = htmlEntityAmpRe.ReplaceAllString(name, "&")
	}
	return strings.TrimSpace(bookNameSuffixRe.ReplaceAllString(name, ""))
}

func formatAuthor(author string) string {
	return strings.TrimSpace(authorPrefixRe.ReplaceAllString(strings.TrimSpace(author), ""))
}

func compileSafe(pattern string) *regexp.Regexp {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}

// fetchWithRule 通过 URL 规则请求（目录/正文翻页共用）。
func (s *Service) fetchWithRule(ctx context.Context, bs *model.BookSource, ruleURL, baseURL string, page int, key string) (string, string, error) {
	s.applyRateLimit(bs)
	au, err := analyzer.NewAnalyzeUrl(ruleURL, key, page, baseURL, bs, s.env.Engine(), nil, s.env.Cookies(), nil)
	if err != nil {
		return "", "", err
	}
	return s.fetch(ctx, au)
}

// ---------------- 搜索 ----------------

// SearchOne 单源搜索（为结果标注响应时间，供来源对比）。
func (s *Service) SearchOne(ctx context.Context, bs *model.BookSource, key string, page int) ([]*model.SearchOrigin, error) {
	if bs.SearchURL == "" {
		return nil, fmt.Errorf("书源 %s 未配置搜索 URL", bs.BookSourceName)
	}
	start := time.Now()
	body, finalURL, err := s.fetchWithRule(ctx, bs, bs.SearchURL, bs.BookSourceURL, page, key)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, `Get "":`) {
			return nil, fmt.Errorf("搜索 URL 解析为空（源可能需要登录、代理或已失效）")
		}
		return nil, fmt.Errorf("搜索请求失败: %w", err)
	}
	items, err := s.analyzeSearchList(bs, body, finalURL, key)
	elapsed := time.Since(start).Milliseconds()
	for _, it := range items {
		it.RespondTime = elapsed
	}
	return items, err
}

// analyzeSearchList 解析搜索结果列表。
func (s *Service) analyzeSearchList(bs *model.BookSource, body, baseURL, key string) ([]*model.SearchOrigin, error) {
	rs := bs.RuleSearch
	if rs == nil || strings.TrimSpace(rs.BookList) == "" {
		return nil, fmt.Errorf("书源 %s 未配置搜索结果规则", bs.BookSourceName)
	}
	ar := s.NewAnalyzeRule(bs, baseURL, nil)
	ar.SetContent(body, baseURL)
	ar.SetRedirectURL(baseURL)
	ar.Bind("key", key)

	reverse := false
	ruleList := rs.BookList
	if strings.HasPrefix(ruleList, "-") {
		reverse = true
		ruleList = ruleList[1:]
	} else if strings.HasPrefix(ruleList, "+") {
		ruleList = ruleList[1:]
	}

	elements := ar.GetElements(ruleList)
	// 详情页捷径：bookUrlPattern 命中当前 URL 时按详情页解析（legado BookList 语义）
	if len(elements) == 0 && bs.BookURLPattern != "" {
		if re := compileSafe(bs.BookURLPattern); re != nil && re.MatchString(baseURL) {
			book, err := s.analyzeBookInfo(bs, body, baseURL)
			if err == nil && book != nil {
				return []*model.SearchOrigin{{
					SourceURL:     bs.BookSourceURL,
					SourceName:    bs.BookSourceName,
					BookURL:       book.BookURL,
					Name:          book.Name,
					Author:        book.Author,
					LatestChapter: book.LatestChapter,
					CoverURL:      book.CoverURL,
					Intro:         book.Intro,
				}}, nil
			}
		}
		return nil, fmt.Errorf("搜索结果解析为空")
	}

	out := make([]*model.SearchOrigin, 0, len(elements))
	seen := map[string]bool{} // 源内 bookUrl 去重（legado LinkedHashSet 语义）
	for _, el := range elements {
		item := s.searchItem(bs, el, baseURL, key)
		if item == nil {
			continue
		}
		if seen[item.BookURL] {
			continue
		}
		seen[item.BookURL] = true
		out = append(out, item)
	}
	if reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// searchItem 解析单个搜索结果。
func (s *Service) searchItem(bs *model.BookSource, el any, baseURL, key string) *model.SearchOrigin {
	rs := bs.RuleSearch
	if rs == nil {
		return nil
	}
	itemAR := s.NewAnalyzeRule(bs, baseURL, nil)
	itemAR.SetContent(el, baseURL)
	itemAR.SetRedirectURL(baseURL)

	name := formatBookName(itemAR.GetString(rs.Name))
	if name == "" {
		return nil
	}
	author := formatAuthor(itemAR.GetString(rs.Author))
	// 精确过滤：name/author/kind 均不含关键字时丢弃（legado precisionSearch）
	if key != "" {
		kind := itemAR.GetString(rs.Kind)
		if !strings.Contains(name, key) && !strings.Contains(author, key) && !strings.Contains(kind, key) {
			return nil
		}
	}
	bookURL := itemAR.GetStringURL(rs.BookURL, nil)
	if bookURL == "" {
		bookURL = baseURL
	}
	intro := content.FormatIntro(itemAR.GetString(rs.Intro))
	if r := []rune(intro); len(r) > 300 {
		intro = string(r[:300]) + "…"
	}
	return &model.SearchOrigin{
		SourceURL:     bs.BookSourceURL,
		SourceName:    bs.BookSourceName,
		BookURL:       bookURL,
		Name:          name,
		Author:        author,
		Kind:          itemAR.GetString(rs.Kind),
		LatestChapter: itemAR.GetString(rs.LastChapter),
		CoverURL:      itemAR.GetStringURL(rs.CoverURL, nil),
		Intro:         intro,
		WordCount:     itemAR.GetString(rs.WordCount),
	}
}

// ---------------- 聚合搜索 ----------------

// Aggregated 聚合搜索结果。
type Aggregated struct {
	Items     []*model.SearchResult `json:"items"`
	Searched  int                   `json:"searched"`
	Total     int                   `json:"total"`
	Errors    []SourceError         `json:"errors,omitempty"`
	ElapsedMS int64                 `json:"elapsedMs"`
}

// SourceError 单源错误。
type SourceError struct {
	Source string `json:"source"`
	Err    string `json:"err"`
}

// Search 聚合搜索（并发 + 书名/作者去重合并 origins，legado SearchModel 语义）。
// onProgress 在每个源完成时回调完整快照（可安全序列化）；未配置搜索 URL 的源不计入总数。
func (s *Service) Search(ctx context.Context, sources []*model.BookSource, key string, onProgress func(*Aggregated)) *Aggregated {
	start := time.Now()
	searchable := make([]*model.BookSource, 0, len(sources))
	for _, bs := range sources {
		if bs != nil && strings.TrimSpace(bs.SearchURL) != "" {
			searchable = append(searchable, bs)
		}
	}
	total := len(searchable)
	agg := &Aggregated{Total: total}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, s.SourceConcurrency))

	for _, bs := range searchable {
		wg.Add(1)
		go func(bs *model.BookSource) {
			defer wg.Done()
			// 单个书源的规则缺陷不允许杀死整个进程（legado runCatching 语义）
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					agg.Searched++
					agg.Errors = append(agg.Errors, SourceError{
						Source: bs.BookSourceName, Err: fmt.Sprintf("书源规则异常: %v", r),
					})
					mu.Unlock()
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			items, err := s.SearchOne(sctx, bs, key, 1)
			mu.Lock()
			agg.Searched++
			if err != nil {
				agg.Errors = append(agg.Errors, SourceError{Source: bs.BookSourceName, Err: err.Error()})
			} else if len(items) > 0 {
				agg.Items = mergeSearchItems(agg.Items, items)
			}
			if onProgress != nil {
				onProgress(snapshotAgg(agg))
			}
			mu.Unlock()
		}(bs)
	}
	wg.Wait()
	agg.ElapsedMS = time.Since(start).Milliseconds()

	// 桶内按命中源数排序（同名书被越多源命中越靠前）
	sort.SliceStable(agg.Items, func(i, j int) bool {
		return len(agg.Items[i].Origins) > len(agg.Items[j].Origins)
	})
	return agg
}

// mergeSearchItems 合并搜索结果：书名+作者去重，同书 origins 聚合。
func mergeSearchItems(existing []*model.SearchResult, incoming []*model.SearchOrigin) []*model.SearchResult {
	for _, in := range incoming {
		key := util.NormalizeBookKey(in.Name, in.Author)
		matched := false
		for _, ex := range existing {
			if util.NormalizeBookKey(ex.Name, ex.Author) == key {
				// origin 按 sourceUrl 去重（legado origins LinkedHashSet 语义）
				dup := false
				for _, o := range ex.Origins {
					if o.SourceURL == in.SourceURL {
						dup = true
						break
					}
				}
				if !dup {
					ex.Origins = append(ex.Origins, *in)
				}
				matched = true
				break
			}
		}
		if !matched {
			existing = append(existing, &model.SearchResult{
				Name:          in.Name,
				Author:        in.Author,
				Kind:          in.Kind,
				CoverURL:      in.CoverURL,
				Intro:         in.Intro,
				LatestChapter: in.LatestChapter,
				WordCount:     in.WordCount,
				Origins:       []model.SearchOrigin{*in},
			})
		}
	}
	return existing
}

// ---------------- 详情 ----------------

// analyzeBookInfo 解析详情页。
func (s *Service) analyzeBookInfo(bs *model.BookSource, body, baseURL string) (*model.Book, error) {
	ri := bs.RuleBookInfo
	ar := s.NewAnalyzeRule(bs, baseURL, nil)
	ar.SetContent(body, baseURL)
	ar.SetRedirectURL(baseURL)
	if ri != nil && strings.TrimSpace(ri.Init) != "" {
		// init 规则切换解析根（JS 取 JSON 场景）
		if initEl := ar.GetElement(ri.Init); initEl != nil {
			ar.SetContent(initEl, baseURL)
		}
	}
	name := formatBookName(ar.GetString(ruleOrEmpty(ri, "name")))
	if name == "" {
		return nil, fmt.Errorf("详情解析书名为空")
	}
	author := formatAuthor(ar.GetString(ruleOrEmpty(ri, "author")))
	book := &model.Book{
		Name:   name,
		Author: author,
		Origin: bs.BookSourceURL,
	}
	book.Intro = content.FormatIntro(ar.GetString(ruleOrEmpty(ri, "intro")))
	book.Kind = ar.GetString(ruleOrEmpty(ri, "kind"))
	book.LatestChapter = ar.GetString(ruleOrEmpty(ri, "lastChapter"))
	book.CoverURL = ar.GetStringURL(ruleOrEmpty(ri, "coverUrl"), nil)
	book.WordCount = ar.GetString(ruleOrEmpty(ri, "wordCount"))
	tocURL := ar.GetStringURL(ruleOrEmpty(ri, "tocUrl"), nil)
	if tocURL == "" || tocURL == baseURL {
		tocURL = baseURL
	}
	book.TocURL = tocURL
	book.BookURL = baseURL
	return book, nil
}

// GetBookInfo 获取详情（请求详情页并解析）。
func (s *Service) GetBookInfo(ctx context.Context, bs *model.BookSource, bookURL string) (*model.Book, error) {
	body, finalURL, err := s.fetchWithRule(ctx, bs, bookURL, bs.BookSourceURL, 1, "")
	if err != nil {
		return nil, fmt.Errorf("详情请求失败: %w", err)
	}
	book, err := s.analyzeBookInfo(bs, body, finalURL)
	if err != nil {
		return nil, err
	}
	book.OriginName = bs.BookSourceName
	return book, nil
}

func ruleOrEmpty(ri *model.RuleBookInfo, field string) string {
	if ri == nil {
		return ""
	}
	switch field {
	case "name":
		return ri.Name
	case "author":
		return ri.Author
	case "intro":
		return ri.Intro
	case "kind":
		return ri.Kind
	case "lastChapter":
		return ri.LastChapter
	case "coverUrl":
		return ri.CoverURL
	case "tocUrl":
		return ri.TocURL
	case "wordCount":
		return ri.WordCount
	}
	return ""
}

// ---------------- 目录 ----------------

// GetToc 获取目录（nextTocUrl 翻页 + URL 去重 + 倒序，legado BookChapterList 语义）。
func (s *Service) GetToc(ctx context.Context, bs *model.BookSource, book *model.Book) ([]*model.Chapter, error) {
	rt := bs.RuleToc
	if rt == nil || strings.TrimSpace(rt.ChapterList) == "" {
		return nil, fmt.Errorf("书源 %s 未配置目录规则", bs.BookSourceName)
	}
	tocURL := book.TocURL
	if tocURL == "" {
		tocURL = book.BookURL
	}
	body, finalURL, err := s.fetchWithRule(ctx, bs, tocURL, book.BookURL, 1, "")
	if err != nil {
		return nil, fmt.Errorf("目录请求失败: %w", err)
	}

	var allElements []any
	elements, err := s.parseTocPage(bs, body, finalURL)
	if err != nil {
		return nil, err
	}
	allElements = append(allElements, elements...)

	// nextTocUrl 翻页：单条串行（防环）；多条并发
	if strings.TrimSpace(rt.NextTocURL) != "" {
		nextURLs := s.nextURLs(bs, body, finalURL, rt.NextTocURL)
		if len(nextURLs) == 1 {
			visited := map[string]bool{finalURL: true}
			next := nextURLs[0]
			for next != "" && !visited[next] {
				visited[next] = true
				pageBody, pageURL, err := s.fetchWithRule(ctx, bs, next, finalURL, 1, "")
				if err != nil {
					break
				}
				pageEls, err := s.parseTocPage(bs, pageBody, pageURL)
				if err != nil {
					break
				}
				allElements = append(allElements, pageEls...)
				n := s.nextURLs(bs, pageBody, pageURL, rt.NextTocURL)
				if len(n) == 0 {
					break
				}
				next = n[0]
			}
		} else if len(nextURLs) > 1 {
			var pageMu sync.Mutex
			var wg sync.WaitGroup
			sem := make(chan struct{}, max(1, s.PageConcurrency))
			results := make([][]any, len(nextURLs))
			for i, u := range nextURLs {
				wg.Add(1)
				go func(i int, u string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					pb, pu, err := s.fetchWithRule(ctx, bs, u, finalURL, 1, "")
					if err != nil {
						return
					}
					els, err := s.parseTocPage(bs, pb, pu)
					if err != nil {
						return
					}
					pageMu.Lock()
					results[i] = els
					pageMu.Unlock()
				}(i, u)
			}
			wg.Wait()
			for _, r := range results {
				allElements = append(allElements, r...)
			}
		}
	}

	// 解析每个元素为章节
	reverse := false
	rule := rt.ChapterList
	if strings.HasPrefix(rule, "-") {
		reverse = true
	}
	chapters := s.parseChapters(bs, allElements, finalURL, book)

	// legado：无 "-" 前缀时列表整体先反转（网页目录常倒序），重复 URL 保留后出现者
	if !reverse {
		util.ReverseAny(chapters)
	}
	// URL 去重（反转后先出现者 = 采集序后出现者，对齐 legado LinkedHashSet 行为）
	dedup := make([]*model.Chapter, 0, len(chapters))
	seen := map[string]bool{}
	for _, ch := range chapters {
		if seen[ch.URL] {
			continue
		}
		seen[ch.URL] = true
		dedup = append(dedup, ch)
	}
	chapters = dedup
	// 用户"目录倒序"开关默认关闭：恢复采集顺序（legado list.reverse()）
	// 净效果：无 "-" 前缀 → 最终顺序 = 解析顺序；带 "-" 前缀 → 逆序
	util.ReverseAny(chapters)
	return chapters, nil
}

// parseTocPage 解析单页目录元素。
func (s *Service) parseTocPage(bs *model.BookSource, body, baseURL string) ([]any, error) {
	rt := bs.RuleToc
	rule := rt.ChapterList
	if strings.HasPrefix(rule, "-") {
		rule = rule[1:]
	} else if strings.HasPrefix(rule, "+") {
		rule = rule[1:]
	}
	ar := s.NewAnalyzeRule(bs, baseURL, nil)
	ar.SetContent(body, baseURL)
	ar.SetRedirectURL(baseURL)
	elements := ar.GetElements(rule)
	if len(elements) == 0 {
		return nil, fmt.Errorf("目录解析为空")
	}
	return elements, nil
}

// nextURLs 获取下一页 URL 列表。
func (s *Service) nextURLs(bs *model.BookSource, body, baseURL, rule string) []string {
	ar := s.NewAnalyzeRule(bs, baseURL, nil)
	ar.SetContent(body, baseURL)
	ar.SetRedirectURL(baseURL)
	urls := ar.GetStringList(rule, nil, true)
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if u != "" && u != baseURL {
			out = append(out, u)
		}
	}
	return out
}

// parseChapters 解析章节元素列表。
func (s *Service) parseChapters(bs *model.BookSource, elements []any, baseURL string, book *model.Book) []*model.Chapter {
	rt := bs.RuleToc
	out := make([]*model.Chapter, 0, len(elements))
	for i, el := range elements {
		ar := s.NewAnalyzeRule(bs, baseURL, nil)
		ar.SetContent(el, baseURL)
		ar.SetRedirectURL(baseURL)

		title := strings.TrimSpace(ar.GetString(rt.ChapterName))
		if title == "" {
			continue
		}
		ch := &model.Chapter{Title: title}
		isVolume := false
		if strings.TrimSpace(rt.IsVolume) != "" {
			v := strings.ToLower(ar.GetString(rt.IsVolume))
			isVolume = v == "true" || v == "1" || v == "yes"
		}
		ch.IsVolume = isVolume
		if isVolume {
			// 卷名无 URL（legado：用 title+index 占位）
			ch.URL = fmt.Sprintf("%s#%d", title, i)
			ch.Tag = ar.GetString(rt.UpdateTime)
		} else {
			url := ar.GetStringURL(rt.ChapterURL, nil)
			if url == "" {
				url = baseURL
			}
			ch.URL = url
			ch.Tag = ar.GetString(rt.UpdateTime)
		}
		if v := strings.ToLower(ar.GetString(rt.IsVip)); v == "true" || v == "1" {
			ch.IsVip = true
		}
		// formatJs 批量格式化标题
		if strings.TrimSpace(rt.FormatJS) != "" {
			ar2 := s.NewAnalyzeRule(bs, baseURL, nil)
			ar2.Bind("index", i+1)
			ar2.Bind("title", ch.Title)
			if out2 := ar2.EvalJS(rt.FormatJS, ch.Title); out2 != nil {
				if s2 := fmt.Sprintf("%v", out2); strings.TrimSpace(s2) != "" {
					ch.Title = s2
				}
			}
		}
		out = append(out, ch)
	}
	return out
}

// ---------------- 正文 ----------------

// GetContent 获取章节正文（nextContentUrl 翻页 + 净化管线）。
func (s *Service) GetContent(ctx context.Context, bs *model.BookSource, book *model.Book, chapter *model.Chapter) (string, error) {
	rc := bs.RuleContent
	if rc == nil || strings.TrimSpace(rc.Content) == "" {
		return "", fmt.Errorf("书源 %s 未配置正文规则", bs.BookSourceName)
	}
	if chapter.IsVolume {
		return "", nil // 卷名不解析正文
	}

	// 下一章 URL（终止翻页判断用）
	nextChapterURL := s.nextChapterURL(bs, book, chapter)

	body, finalURL, err := s.fetchWithRule(ctx, bs, chapter.URL, book.TocURL, 1, "")
	if err != nil {
		return "", fmt.Errorf("正文请求失败: %w", err)
	}

	pages := []string{body}
	visited := map[string]bool{finalURL: true}
	// nextContentUrl 翻页
	nextURLs := s.contentNextURLs(bs, body, finalURL, rc.NextContentURL)
	if len(nextURLs) == 1 {
		next := nextURLs[0]
		for next != "" && !visited[next] {
			if nextChapterURL != "" && util.SameURL(next, nextChapterURL) {
				break // 下一页 == 下一章：终止
			}
			visited[next] = true
			pageBody, pageURL, err := s.fetchWithRule(ctx, bs, next, finalURL, 1, "")
			if err != nil {
				break
			}
			pages = append(pages, pageBody)
			n := s.contentNextURLs(bs, pageBody, pageURL, rc.NextContentURL)
			if len(n) == 0 {
				break
			}
			next = n[0]
		}
	} else if len(nextURLs) > 1 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, max(1, s.PageConcurrency))
		results := make([]string, len(nextURLs))
		for i, u := range nextURLs {
			wg.Add(1)
			go func(i int, u string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				pb, _, err := s.fetchWithRule(ctx, bs, u, finalURL, 1, "")
				if err != nil {
					return
				}
				results[i] = pb
			}(i, u)
		}
		wg.Wait()
		for _, r := range results {
			if r != "" {
				pages = append(pages, r)
			}
		}
	}

	// 每页解析正文后拼接
	var full strings.Builder
	for _, pageBody := range pages {
		pageText := s.analyzeContentPage(bs, pageBody, finalURL, chapter)
		if pageText != "" {
			if full.Len() > 0 {
				full.WriteString("\n")
			}
			full.WriteString(pageText)
		}
	}
	text := full.String()

	// 源级 replaceRegex（作为规则串执行）
	if strings.TrimSpace(rc.ReplaceRegex) != "" {
		ar := s.NewAnalyzeRule(bs, finalURL, nil)
		ar.SetContent(text, finalURL)
		if replaced := ar.GetString(rc.ReplaceRegex); strings.TrimSpace(replaced) != "" {
			text = replaced
		}
	}

	// 净化替换规则
	if s.ReplaceProvider != nil {
		p := replace.NewProcessor(s.ReplaceProvider(), s.env.Engine(), nil)
		text = p.ApplyContent(text, book.Name, book.Origin)
	}

	// 不可见字符清理
	text = util.CleanInvisibleChars(text)
	// 去空白行
	text = util.RemoveBlankLines(text)
	return text, nil
}

// analyzeContentPage 解析单页正文（legado BookContent 语义：
// unescape=false → HtmlFormatter.formatKeepImg → 含 & 时 unescapeHtml4）。
func (s *Service) analyzeContentPage(bs *model.BookSource, body, baseURL string, chapter *model.Chapter) string {
	rc := bs.RuleContent
	ar := s.NewAnalyzeRule(bs, baseURL, nil)
	ar.SetContent(body, baseURL)
	ar.SetRedirectURL(baseURL)

	raw := ar.GetStringUnescape(rc.Content, false)
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	// 格式化（保留图片，src 绝对化）
	formatted := content.FormatKeepImg(raw, baseURL)
	if strings.Contains(formatted, "&") {
		formatted = analyzer.UnescapeHTML(formatted)
	}
	return formatted
}

// contentNextURLs 正文下一页 URL 列表。
func (s *Service) contentNextURLs(bs *model.BookSource, body, baseURL, rule string) []string {
	if strings.TrimSpace(rule) == "" {
		return nil
	}
	ar := s.NewAnalyzeRule(bs, baseURL, nil)
	ar.SetContent(body, baseURL)
	ar.SetRedirectURL(baseURL)
	urls := ar.GetStringList(rule, nil, true)
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if u != "" && u != baseURL {
			out = append(out, u)
		}
	}
	return out
}

// nextChapterURL 查询下一章 URL（目录内 index+1）。
func (s *Service) nextChapterURL(bs *model.BookSource, book *model.Book, chapter *model.Chapter) string {
	if s.NextChapterProvider != nil {
		return s.NextChapterProvider(book, chapter.Index)
	}
	return ""
}

// snapshotAgg 生成可安全跨 goroutine 序列化的聚合快照。
func snapshotAgg(agg *Aggregated) *Aggregated {
	out := &Aggregated{
		Searched:  agg.Searched,
		Total:     agg.Total,
		ElapsedMS: agg.ElapsedMS,
		Errors:    append([]SourceError(nil), agg.Errors...),
	}
	out.Items = make([]*model.SearchResult, 0, len(agg.Items))
	for _, it := range agg.Items {
		if it == nil {
			continue
		}
		cp := *it
		cp.Origins = append([]model.SearchOrigin(nil), it.Origins...)
		out.Items = append(out.Items, &cp)
	}
	return out
}
