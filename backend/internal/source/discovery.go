// discovery.go 内置书源仓库的动态发现：代码只写死两个站点根地址，
// 所有仓库条目（名称/直链/数量/更新时间）部署后运行时抓取——站点换源无需改软件。
// aoaostar：根页静态 HTML，<details> 区块含条目（名称 + 访问直链 + legado:// 一键导入链接 + 条数/更新时间）。
// yiove：SPA，从首页 JS chunk 提取 apiBaseUrl，再调其 JSON API 拉合集列表。
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"booksmith/internal/httpx"
)

// RepoProvider 仓库提供站点（唯一写死的常量：站点根地址）。
type RepoProvider struct {
	Key   string `json:"key"`   // "aoaostar" / "yiove"
	Label string `json:"label"` // 显示名
	Root  string `json:"root"`  // 站点根地址（风险隔离：仅此 URL 硬编码）
}

// BuiltinProviders 内置提供站点清单（仅站点根地址；仓库条目全部运行时发现）。
var BuiltinProviders = []RepoProvider{
	{Key: "aoaostar", Label: "aoaostar「阅读」APP 源", Root: "https://legado.aoaostar.com"},
	{Key: "yiove", Label: "Yiove 书源仓库", Root: "https://shuyuan.yiove.com"},
}

// 仓库类别（书源与净化规则在广场分栏展示，避免混在一起找）。
const (
	RepoCategorySource  = "bookSource"  // 书源仓库
	RepoCategoryReplace = "replaceRule" // 净化规则仓库
)

// discoverTTL 发现结果缓存时长（避免每次打开广场都抓站点）。
const discoverTTL = 5 * time.Minute

// discoveryCache 发现结果缓存（provider key → 条目）。
var discoveryCache = struct {
	mu       sync.Mutex
	items    map[string]cachedDiscovery
}{items: map[string]cachedDiscovery{}}

type cachedDiscovery struct {
	result    *ProviderResult
	fetchedAt time.Time
}

// ProviderResult 单个提供站点的发现结果（失败时 Err 非空、Repos 为空）。
// Repos 按 Kind 分为两类：bookSource（书源仓库）与 replaceRule（净化规则仓库），
// 前端据此分栏展示；SourceList 为 yiove 全量书源列表的元信息（分页拉取，不一次全量）。
type ProviderResult struct {
	Key        string       `json:"key"`
	Label      string       `json:"label"`
	Root       string       `json:"root"`
	Repos      []SourceRepo `json:"repos"`
	Err        string       `json:"err,omitempty"`
	ElapsedMS  int64        `json:"elapsedMs"`
	Cached     bool         `json:"cached"`
	SourceList *SourceListMeta `json:"sourceList,omitempty"` // yiove 独有：全量书源列表元信息
}

// SourceListMeta yiove 单源列表元信息（3 万+ 单源分页发现）。
type SourceListMeta struct {
	APIBase string `json:"apiBase"` // 单源直链前缀（/import/book-sources/{start}-{end}）
	Total   int    `json:"total"`   // 站点书源总数
	Pages   int    `json:"pages"`   // 按 pageSize 分页数
}

// discoverCacheGet 命中未过期缓存。
func discoverCacheGet(key string) (*ProviderResult, bool) {
	discoveryCache.mu.Lock()
	defer discoveryCache.mu.Unlock()
	c, ok := discoveryCache.items[key]
	if !ok || time.Since(c.fetchedAt) > discoverTTL {
		return nil, false
	}
	return c.result, true
}

func discoverCachePut(key string, result *ProviderResult) {
	discoveryCache.mu.Lock()
	defer discoveryCache.mu.Unlock()
	discoveryCache.items[key] = cachedDiscovery{result: result, fetchedAt: time.Now()}
}

// fetchText 抓取 URL 文本（复用书源管理器的 httpx 客户端，带重试/限流能力）。
// 附带 Referer：yiove 的导入直链对无 Referer 请求弹 Cloudflare 质询。
// CF 质询页（403/422 + HTML）表现为间歇性，非 200 时间隔 2s 重试一次。
func (m *Manager) fetchText(url string, timeout time.Duration) (string, int, error) {
	for attempt := 0; ; attempt++ {
		resp, err := m.client.Do(nil, &httpx.Request{
			URL:     url,
			Timeout: timeout,
			Headers: map[string]string{
				"Referer": refererOf(url),
				"Accept":  "application/json, text/html, */*",
			},
		})
		if err != nil {
			return "", 0, err
		}
		if resp.StatusCode == 200 {
			return resp.BodyText(), resp.StatusCode, nil
		}
		text := resp.BodyText()
		// 非成功状态：重试一次（CF 质询是间歇性的，第二次常通过）
		if attempt < 1 && resp.StatusCode != 404 {
			time.Sleep(2 * time.Second)
			continue
		}
		return text, resp.StatusCode, nil
	}
}

// refererOf 由 URL 推导 Referer（scheme://host/）。
func refererOf(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		host := raw[i+3:]
		if j := strings.Index(host, "/"); j >= 0 {
			host = host[:j]
		}
		return raw[:i] + "://" + host + "/"
	}
	return ""
}

// DiscoverProvider 发现单个站点（带缓存）。
func (m *Manager) DiscoverProvider(p RepoProvider) (*ProviderResult, error) {
	if res, ok := discoverCacheGet(p.Key); ok {
		return res, nil
	}
	var res *ProviderResult
	var err error
	switch p.Key {
	case "aoaostar":
		res, err = m.discoverAoaostar(p)
	case "yiove":
		res, err = m.discoverYiove(p)
	default:
		return nil, fmt.Errorf("未知仓库站点: %s", p.Key)
	}
	if err != nil {
		return nil, err
	}
	total := len(res.Repos)
	if total == 0 && res.SourceList == nil {
		return nil, fmt.Errorf("站点未发现任何书源仓库条目（结构可能已变化）")
	}
	discoverCachePut(p.Key, res)
	return res, nil
}

// DiscoverAll 并发发现全部内置站点；单个站点失败不影响其他（错误放在各自结果里）。
func (m *Manager) DiscoverAll() []ProviderResult {
	out := make([]ProviderResult, len(BuiltinProviders))
	var wg sync.WaitGroup
	for i, p := range BuiltinProviders {
		wg.Add(1)
		go func(i int, p RepoProvider) {
			defer wg.Done()
			res := ProviderResult{Key: p.Key, Label: p.Label, Root: p.Root, Repos: []SourceRepo{}}
			start := time.Now()
			if cached, ok := discoverCacheGet(p.Key); ok {
				res = *cached
				res.Cached = true
			} else if r, err := m.DiscoverProvider(p); err != nil {
				res.Err = err.Error()
			} else {
				res = *r
			}
			res.ElapsedMS = time.Since(start).Milliseconds()
			out[i] = res
		}(i, p)
	}
	wg.Wait()
	return out
}

// ---------------- aoaostar：静态 HTML 解析 ----------------

// aoaostar 条目结构（真实页面实测）：
// <ul><li><p>名称</p><ul>
//   <li><a href="…/sources/xxx.json">访问直链</a></li>
//   <li><a href="legado://import/bookSource?src=…">一键导入</a></li>
//   <li>上一次同步状态: 同步成功, 共 3907 条</li>
//   <li>更新时间: 2026-08-15 08:42:58</li>
// </ul></li></ul>

// discoverAoaostar 抓根页并解析书源/净化规则条目（rss/theme/tts 不在本软件范围，忽略）。
func (m *Manager) discoverAoaostar(p RepoProvider) (*ProviderResult, error) {
	text, status, err := m.fetchText(strings.TrimRight(p.Root, "/")+"/", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("抓取 %s 失败: %w", p.Root, err)
	}
	if status != 200 {
		return nil, fmt.Errorf("抓取 %s: HTTP %d", p.Root, status)
	}
	return &ProviderResult{
		Key: p.Key, Label: p.Label, Root: p.Root,
		Repos: parseAoaostarHTML(text),
	}, nil
}

// parseAoaostarHTML 解析 aoaostar 首页 HTML 中的仓库条目。
func parseAoaostarHTML(body string) []SourceRepo {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	var out []SourceRepo
	walkAoaostar(doc, &out)
	return out
}

// walkAoaostar 深度优先遍历 DOM，按"直链 + 导入链接"配对提取条目。
func walkAoaostar(n *html.Node, out *[]SourceRepo) {
	if n.Type != html.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkAoaostar(c, out)
		}
		return
	}
	// 条目单位 = 包含"访问直链"链接的最外层 li
	if n.Data == "li" {
		direct := ""
		kind := ""
		title := ""
		total := 0
		collectAoaostarItem(n, &direct, &kind, &title, &total)
		if direct != "" && kind != "" && title != "" {
			note := ""
			if total > 0 {
				note = fmt.Sprintf("共 %d 条", total)
			}
			*out = append(*out, SourceRepo{
				Name:     strings.TrimSpace(title),
				URL:      direct,
				Kind:     kind,
				Note:     note,
				Provider: "aoaostar",
			})
			return // 条目内不再下钻
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkAoaostar(c, out)
	}
}

// collectAoaostarItem 从 li 子树提取直链/kind/标题/条数。
func collectAoaostarItem(li *html.Node, direct, kind, title *string, total *int) {
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "a":
				href := ""
				for _, a := range n.Attr {
					if a.Key == "href" {
						href = a.Val
					}
				}
				text := strings.TrimSpace(textContent(n))
				if strings.HasPrefix(href, "legado://import/") {
					*kind = kindFromLegadoScheme(href)
				} else if text == "访问直链" && *direct == "" {
					*direct = href
				}
			case "p":
				if *title == "" {
					t := strings.TrimSpace(textContent(n))
					if t != "" && t != "访问直链" && t != "一键导入" && !strings.HasPrefix(t, "上一次同步") && !strings.HasPrefix(t, "更新时间") {
						*title = t
					}
				}
			}
		}
		// 文本节点匹配"共 N 条"
		if n.Type == html.TextNode {
			t := strings.TrimSpace(n.Data)
			if strings.Contains(t, "共") && strings.HasSuffix(t, "条") {
				if mm := aoaostarTotalRe.FindStringSubmatch(t); mm != nil {
					fmt.Sscanf(mm[1], "%d", total)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(li)
}

var aoaostarTotalRe = regexp.MustCompile(`共\s*(\d+)\s*条`)

// kindFromLegadoScheme legado://import/bookSource|rssSource|replaceRule?src=… → kind。
func kindFromLegadoScheme(href string) string {
	switch {
	case strings.Contains(href, "import/bookSource?"):
		return "bookSource"
	case strings.Contains(href, "import/replaceRule?"):
		return "replaceRule"
	default:
		return "" // rssSource/readConfig/theme/httpTTS 不在本软件范围
	}
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// ---------------- yiove：SPA JS chunk 提取 API base → JSON API ----------------

// yioveCollectionsAPI 合集列表 API 路径（相对 apiBaseUrl）。
const yioveCollectionsAPI = "/shuyuan/book-source-collections?page=1&page_size=100"

// discoverYiove 发现 yiove：合集列表 + 全量单源列表元信息（书源/净化规则分开由 Kind/SourceList 区分）。
func (m *Manager) discoverYiove(p RepoProvider) (*ProviderResult, error) {
	apiBase, err := m.yioveAPIBase(p.Root)
	if err != nil {
		return nil, err
	}
	// 合集（精选聚合，导入价值最高）
	text, status, err := m.fetchText(strings.TrimRight(apiBase, "/")+yioveCollectionsAPI, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("抓取 yiove 合集失败: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("抓取 yiove 合集: HTTP %d", status)
	}
	repos, err := parseYioveCollections(apiBase, []byte(text))
	if err != nil {
		return nil, err
	}
	res := &ProviderResult{
		Key: p.Key, Label: p.Label, Root: p.Root,
		Repos: repos,
	}
	// 全量单源列表元信息（3 万+，分页按需拉取，不一次全量）
	if meta, err := m.yioveSourceListMeta(apiBase); err == nil && meta != nil {
		res.SourceList = meta
	}
	return res, nil
}

// yioveSourceListMeta 拉单源列表第一页获取 total/pages（pageSize=100）。
func (m *Manager) yioveSourceListMeta(apiBase string) (*SourceListMeta, error) {
	text, status, err := m.fetchText(strings.TrimRight(apiBase, "/")+"/shuyuan/book-sources?page=1&page_size=100", 30*time.Second)
	if err != nil || status != 200 {
		return nil, fmt.Errorf("抓取 yiove 书源列表元信息失败: HTTP %d", status)
	}
	var resp struct {
		Total int `json:"total"`
		Pages int `json:"pages"`
	}
	if err := jsonUnmarshalStrict([]byte(text), &resp); err != nil {
		return nil, fmt.Errorf("解析 yiove 书源列表: %w", err)
	}
	if resp.Total <= 0 {
		return nil, fmt.Errorf("yiove 书源列表为空")
	}
	return &SourceListMeta{
		APIBase: strings.TrimRight(apiBase, "/"),
		Total:   resp.Total,
		Pages:   resp.Pages,
	}, nil
}

// yioveAPIBase 从站点首页 JS chunk 提取 API base（风险隔离：不写死 API 地址）。
// 提取失败回退同根域约定 https://shuyuan-api.<host>。
func (m *Manager) yioveAPIBase(root string) (string, error) {
	home, status, err := m.fetchText(strings.TrimRight(root, "/")+"/", 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("抓取 %s 失败: %w", root, err)
	}
	if status != 200 {
		return "", fmt.Errorf("抓取 %s: HTTP %d", root, status)
	}
	// 收集 <script src> 资源
	doc, perr := html.Parse(strings.NewReader(home))
	if perr != nil {
		return "", perr
	}
	var scripts []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			for _, a := range n.Attr {
				if a.Key == "src" {
					scripts = append(scripts, a.Val)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	for _, src := range scripts {
		if !strings.Contains(src, "assets/") && !strings.Contains(src, ".js") {
			continue
		}
		u := src
		if strings.HasPrefix(u, "/") {
			u = strings.TrimRight(root, "/") + u
		}
		js, st, err := m.fetchText(u, 30*time.Second)
		if err != nil || st != 200 {
			continue
		}
		for _, re := range []*regexp.Regexp{
			regexp.MustCompile(`apiBaseUrl\s*[:=]\s*"([^"]+)"`),
			regexp.MustCompile(`VITE_API_BASE_URL\s*[:=]\s*"([^"]+)"`),
		} {
			if mm := re.FindStringSubmatch(js); mm != nil && mm[1] != "" {
				return strings.TrimRight(mm[1], "/"), nil
			}
		}
	}
	// 回退：同根域约定（shuyuan.yiove.com → shuyuan-api.yiove.com）
	host := strings.TrimPrefix(strings.TrimPrefix(root, "https://"), "http://")
	if dot := strings.Index(host, "."); dot > 0 {
		fallback := "https://shuyuan-api." + host[dot+1:]
		return fallback, nil
	}
	return "", fmt.Errorf("未能从站点前端提取 API 地址")
}

// yioveCollectionItem 合集 API 单条。
type yioveCollectionItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Total       int     `json:"total"`
	CreateTime  float64 `json:"create_time"`
}

// parseYioveCollections 解析合集列表 JSON → SourceRepo 列表。
func parseYioveCollections(apiBase string, data []byte) ([]SourceRepo, error) {
	var resp struct {
		Items []yioveCollectionItem `json:"items"`
		Total int                   `json:"total"`
	}
	if err := jsonUnmarshalStrict(data, &resp); err != nil {
		return nil, fmt.Errorf("解析 yiove 合集 JSON: %w", err)
	}
	out := make([]SourceRepo, 0, len(resp.Items))
	for _, it := range resp.Items {
		if it.ID == "" || it.Name == "" {
			continue
		}
		note := it.Description
		if note == "" {
			note = fmt.Sprintf("共 %d 条", it.Total)
		} else if it.Total > 0 {
			note = fmt.Sprintf("%s（共 %d 条）", note, it.Total)
		}
		out = append(out, SourceRepo{
			Name:        it.Name,
			URL:         strings.TrimRight(apiBase, "/") + "/import/book-source-collection/" + it.ID,
			Kind:        "bookSource",
			Note:        note,
			Provider:    "yiove",
			CollectionID: it.ID,
			Total:       it.Total,
			UpdatedAt:   int64(it.CreateTime),
		})
	}
	return out, nil
}

// jsonUnmarshalStrict 解析 JSON（封装标准库，保持调用点命名一致）。
func jsonUnmarshalStrict(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// ---------------- yiove 单源列表（分页发现 + 搜索） ----------------

// YioveSourceItem 单源列表条目（元数据，不含规则本体）。
type YioveSourceItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"` // 书源目标站点
	IsValid   bool   `json:"isValid"`
	ViewTotal int    `json:"viewTotal"`
	UpdatedAt int64  `json:"updatedAt"`
}

// YioveSourcePage 单源列表分页结果。
type YioveSourcePage struct {
	Items []YioveSourceItem `json:"items"`
	Page  int               `json:"page"`
	Pages int               `json:"pages"`
	Total int               `json:"total"`
}

// yioveListResponse 站点列表 API 原始结构。
type yioveListResponse struct {
	Items []struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		URL        string   `json:"url"`
		IsValid    bool     `json:"is_valid"`
		ViewTotal  int      `json:"view_total"`
		CreateTime float64  `json:"create_time"`
	} `json:"items"`
	Page  int `json:"page"`
	Pages int `json:"pages"`
	Total int `json:"total"`
}

// yioveListItemsToItems 通用映射（列表/搜索共用）。
func yioveListItemsToItems(resp *yioveListResponse) *YioveSourcePage {
	out := &YioveSourcePage{Page: resp.Page, Pages: resp.Pages, Total: resp.Total, Items: []YioveSourceItem{}}
	for _, it := range resp.Items {
		if it.ID == "" {
			continue
		}
		out.Items = append(out.Items, YioveSourceItem{
			ID: it.ID, Name: it.Name, URL: it.URL,
			IsValid: it.IsValid, ViewTotal: it.ViewTotal,
			UpdatedAt: int64(it.CreateTime),
		})
	}
	return out
}

// YioveSourceList 拉取 yiove 全量单源列表的一页（apiBase 由发现结果给出）。
func (m *Manager) YioveSourceList(apiBase string, page, pageSize int) (*YioveSourcePage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	text, status, err := m.fetchText(fmt.Sprintf("%s/shuyuan/book-sources?page=%d&page_size=%d", strings.TrimRight(apiBase, "/"), page, pageSize), 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("抓取 yiove 书源列表失败: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("抓取 yiove 书源列表: HTTP %d", status)
	}
	var resp yioveListResponse
	if err := jsonUnmarshalStrict([]byte(text), &resp); err != nil {
		return nil, fmt.Errorf("解析 yiove 书源列表: %w", err)
	}
	return yioveListItemsToItems(&resp), nil
}

// YioveSearch 搜索 yiove（sourceType: book-sources / book-source-collections）。
func (m *Manager) YioveSearch(apiBase, keyword, sourceType string, page, pageSize int) (*YioveSourcePage, error) {
	if sourceType != "book-sources" && sourceType != "book-source-collections" {
		sourceType = "book-sources"
	}
	if keyword == "" {
		return nil, fmt.Errorf("搜索关键词不能为空")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	url := fmt.Sprintf("%s/shuyuan/search?search_key=%s&search_type=%s&page=%d&page_size=%d",
		strings.TrimRight(apiBase, "/"), urlQueryEscape(keyword), sourceType, page, pageSize)
	text, status, err := m.fetchText(url, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("yiove 搜索失败: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("yiove 搜索: HTTP %d", status)
	}
	var resp yioveListResponse
	if err := jsonUnmarshalStrict([]byte(text), &resp); err != nil {
		return nil, fmt.Errorf("解析 yiove 搜索结果: %w", err)
	}
	return yioveListItemsToItems(&resp), nil
}

// urlQueryEscape 查询参数转义。
func urlQueryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.PathEscape(s), "+", "%2B"), " ", "%20")
}

// YioveSourceListImportURL 单源列表页 → 序号段导入直链（如 1-20）。
// 起止序号 = (page-1)*pageSize+1 至 page*pageSize。
func YioveSourceListImportURL(apiBase string, page, pageSize int) string {
	start := (page-1)*pageSize + 1
	end := page * pageSize
	return fmt.Sprintf("%s/import/book-sources/%d-%d", strings.TrimRight(apiBase, "/"), start, end)
}

// DiscoverAllAsync 带上下文的并发发现（预留给将来超时控制）。
var _ = context.Background
