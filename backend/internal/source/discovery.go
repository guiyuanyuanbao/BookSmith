// discovery.go 内置书源仓库的动态发现：代码只写死两个站点根地址，
// 所有仓库条目（名称/直链/数量/更新时间）部署后运行时抓取——站点换源无需改软件。
// aoaostar：根页静态 HTML，<details> 区块含条目（名称 + 访问直链 + legado:// 一键导入链接 + 条数/更新时间）。
// yiove：SPA，从首页 JS chunk 提取 apiBaseUrl，再调其 JSON API 拉合集列表。
package source

import (
	"context"
	"encoding/json"
	"fmt"
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

// discoverTTL 发现结果缓存时长（避免每次打开广场都抓站点）。
const discoverTTL = 5 * time.Minute

// discoveryCache 发现结果缓存（provider key → 条目）。
var discoveryCache = struct {
	mu       sync.Mutex
	items    map[string]cachedDiscovery
}{items: map[string]cachedDiscovery{}}

type cachedDiscovery struct {
	repos     []SourceRepo
	fetchedAt time.Time
}

// ProviderResult 单个提供站点的发现结果（失败时 Err 非空、Repos 为空）。
type ProviderResult struct {
	Key       string       `json:"key"`
	Label     string       `json:"label"`
	Root      string       `json:"root"`
	Repos     []SourceRepo `json:"repos"`
	Err       string       `json:"err,omitempty"`
	ElapsedMS int64        `json:"elapsedMs"`
	Cached    bool         `json:"cached"`
}

// discoverCacheGet 命中未过期缓存。
func discoverCacheGet(key string) ([]SourceRepo, bool) {
	discoveryCache.mu.Lock()
	defer discoveryCache.mu.Unlock()
	c, ok := discoveryCache.items[key]
	if !ok || time.Since(c.fetchedAt) > discoverTTL {
		return nil, false
	}
	return c.repos, true
}

func discoverCachePut(key string, repos []SourceRepo) {
	discoveryCache.mu.Lock()
	defer discoveryCache.mu.Unlock()
	discoveryCache.items[key] = cachedDiscovery{repos: repos, fetchedAt: time.Now()}
}

// fetchText 抓取 URL 文本（复用书源管理器的 httpx 客户端，带重试/限流能力）。
func (m *Manager) fetchText(url string, timeout time.Duration) (string, int, error) {
	resp, err := m.client.Do(nil, &httpx.Request{URL: url, Timeout: timeout})
	if err != nil {
		return "", 0, err
	}
	return resp.BodyText(), resp.StatusCode, nil
}

// DiscoverRepos 发现单个站点的仓库条目（带缓存）。
func (m *Manager) DiscoverRepos(p RepoProvider) ([]SourceRepo, error) {
	if repos, ok := discoverCacheGet(p.Key); ok {
		return repos, nil
	}
	var (
		repos []SourceRepo
		err   error
	)
	switch p.Key {
	case "aoaostar":
		repos, err = m.discoverAoaostar(p.Root)
	case "yiove":
		repos, err = m.discoverYiove(p.Root)
	default:
		return nil, fmt.Errorf("未知仓库站点: %s", p.Key)
	}
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("站点未发现任何书源仓库条目（结构可能已变化）")
	}
	discoverCachePut(p.Key, repos)
	return repos, nil
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
			repos, cached := func() ([]SourceRepo, bool) {
				if repos, ok := discoverCacheGet(p.Key); ok {
					return repos, true
				}
				return nil, false
			}()
			if cached {
				res.Repos = repos
				res.Cached = true
			} else {
				repos, err := m.DiscoverRepos(p)
				if err != nil {
					res.Err = err.Error()
				} else {
					res.Repos = repos
				}
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
func (m *Manager) discoverAoaostar(root string) ([]SourceRepo, error) {
	text, status, err := m.fetchText(strings.TrimRight(root, "/")+"/", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("抓取 %s 失败: %w", root, err)
	}
	if status != 200 {
		return nil, fmt.Errorf("抓取 %s: HTTP %d", root, status)
	}
	return parseAoaostarHTML(text), nil
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

// discoverYiove 发现 yiove 合集列表。
func (m *Manager) discoverYiove(root string) ([]SourceRepo, error) {
	apiBase, err := m.yioveAPIBase(root)
	if err != nil {
		return nil, err
	}
	text, status, err := m.fetchText(strings.TrimRight(apiBase, "/")+yioveCollectionsAPI, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("抓取 yiove 合集失败: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("抓取 yiove 合集: HTTP %d", status)
	}
	return parseYioveCollections(apiBase, []byte(text))
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

// DiscoverAllAsync 带上下文的并发发现（预留给将来超时控制）。
var _ = context.Background
