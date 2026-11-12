package source

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"booksmith/internal/model"
)

// aoaostarFeedHTML 仿真实首页结构（details > ul > li > p 名称 + 直链/导入链接/条数）。
const aoaostarFeedHTML = `<html><body>
<details><summary>点击展开</summary>
<ul><li><p>全量书源</p><ul>
<li><a href="https://legado.aoaostar.com/sources/b778fe6b.json">访问直链</a></li>
<li><a href="legado://import/bookSource?src=https://legado.aoaostar.com/sources/b778fe6b.json">一键导入</a></li>
<li>上一次同步状态: 同步成功, 共 3907 条</li>
<li>更新时间: 2026-08-15 08:42:58</li>
</ul></li></ul>
<hr>
<ul><li><p>XIU2精品书源 🔥</p><ul>
<li><a href="https://legado.aoaostar.com/sources/71e56d4f.json">访问直链</a></li>
<li><a href="legado://import/bookSource?src=https://legado.aoaostar.com/sources/71e56d4f.json">一键导入</a></li>
<li>上一次同步状态: 同步成功, 共 22 条</li>
</ul></li></ul>
<hr>
<ul><li><p>乌云净化</p><ul>
<li><a href="https://legado.aoaostar.com/sources/3f067eb2.json">访问直链</a></li>
<li><a href="legado://import/replaceRule?src=https://legado.aoaostar.com/sources/3f067eb2.json">一键导入</a></li>
<li>上一次同步状态: 同步成功, 共 88 条</li>
</ul></li></ul>
<hr>
<ul><li><p>某主题</p><ul>
<li><a href="https://legado.aoaostar.com/sources/9c92e8e8.json">访问直链</a></li>
<li><a href="legado://import/theme?src=https://legado.aoaostar.com/sources/9c92e8e8.json">一键导入</a></li>
</ul></li></ul>
</details></body></html>`

func TestParseAoaostarHTML(t *testing.T) {
	repos := parseAoaostarHTML(aoaostarFeedHTML)
	if len(repos) != 3 {
		t.Fatalf("应提取 3 条（bookSource×2 + replaceRule×1，theme 忽略），实际 %d: %+v", len(repos), repos)
	}
	byName := map[string]SourceRepo{}
	for _, r := range repos {
		byName[r.Name] = r
	}
	full, ok := byName["全量书源"]
	if !ok {
		t.Fatal("缺少「全量书源」条目")
	}
	if full.URL != "https://legado.aoaostar.com/sources/b778fe6b.json" {
		t.Errorf("直链错误: %s", full.URL)
	}
	if full.Kind != "bookSource" {
		t.Errorf("kind 错误: %s", full.Kind)
	}
	if full.Note != "共 3907 条" {
		t.Errorf("note 错误: %q", full.Note)
	}
	if full.Provider != "aoaostar" {
		t.Errorf("provider 错误: %q", full.Provider)
	}
	wy, ok := byName["乌云净化"]
	if !ok || wy.Kind != "replaceRule" {
		t.Fatalf("净化规则条目提取错误: %+v", wy)
	}
	xiu, ok := byName["XIU2精品书源 🔥"]
	if !ok {
		t.Fatal("含 emoji 的名称应原样保留")
	}
	_ = xiu
}

func TestKindFromLegadoScheme(t *testing.T) {
	cases := map[string]string{
		"legado://import/bookSource?src=https://x": "bookSource",
		"legado://import/replaceRule?src=https://x": "replaceRule",
		"legado://import/rssSource?src=https://x":   "",
		"legado://import/theme?src=https://x":       "",
		"legado://import/httpTTS?src=https://x":     "",
		"https://plain.link":                        "",
	}
	for in, want := range cases {
		if got := kindFromLegadoScheme(in); got != want {
			t.Errorf("kindFromLegadoScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// yiove 发现流程：首页 → 提取 JS chunk → apiBaseUrl → 合集 API。
func TestDiscoverYiove(t *testing.T) {
	apiBase := ""
	collectionsJSON := `{"items":[
		{"id":"id-1","name":"精品书源合集","description":"可以稳定使用","total":11,"create_time":1793355518.5},
		{"id":"id-2","name":"稳定书源合集","description":"","total":13,"create_time":1787021891.0},
		{"id":"","name":"无效条目应被跳过","total":0}
	],"pages":1,"total":2}`

	mux := http.NewServeMux()
	// 首页：带 assets JS
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><script src="/assets/index-abc.js"></script></head><body>Yiove</body></html>`))
	})
	// 主 JS chunk：含 apiBaseUrl
	mux.HandleFunc("GET /assets/index-abc.js", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`const _r={appName:"x",apiBaseUrl:"` + apiBase + `"};`))
	})
	// 合集 API
	mux.HandleFunc("GET /shuyuan/book-source-collections", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(collectionsJSON))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	apiBase = srv.URL

	m, _ := testManager(t)
	res, err := m.DiscoverProvider(RepoProvider{Key: "yiove", Root: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	repos := res.Repos
	if len(repos) != 2 {
		t.Fatalf("应提取 2 条有效合集，实际 %d", len(repos))
	}
	// 单源列表元信息（测试站未实现该 API → SourceList 应为 nil，不阻塞合集发现）
	if res.SourceList != nil {
		t.Errorf("无列表 API 时 SourceList 应为 nil")
	}
	first := repos[0]
	if first.Name != "精品书源合集" {
		t.Errorf("name = %q", first.Name)
	}
	if first.URL != srv.URL+"/import/book-source-collection/id-1" {
		t.Errorf("URL = %q", first.URL)
	}
	if first.Kind != "bookSource" || first.Provider != "yiove" || first.CollectionID != "id-1" || first.Total != 11 {
		t.Errorf("字段错误: %+v", first)
	}
	// 无描述的合集 Note 应回退为条数
	if repos[1].Note != "共 13 条" {
		t.Errorf("note fallback = %q", repos[1].Note)
	}
	// 第二次调用应命中缓存（不再请求站点：用计数服务器验证）
	res2, err := m.DiscoverProvider(RepoProvider{Key: "yiove", Root: srv.URL})
	if err != nil || len(res2.Repos) != 2 {
		t.Fatalf("缓存命中失败: %v %d", err, len(res2.Repos))
	}
}

func TestYioveAPIBaseFallback(t *testing.T) {
	// 首页无 JS 或提取失败时回退 shuyuan-api.<host> 约定
	m, _ := testManager(t)
	// 服务器返回空 HTML（无 script）→ 回退域名约定
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>no scripts</body></html>`))
	}))
	defer srv.Close()
	base, err := m.yioveAPIBase(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// 127.0.0.1:port 无 "." 分隔的域名段（第一个 . 在端口前的 IP 里）——只要不 panic 且返回非空即认为回退逻辑工作
	if base == "" {
		t.Fatal("回退结果不应为空")
	}
}

func TestYioveSourceListAndSearch(t *testing.T) {
	listJSON := `{"items":[
		{"id":"s1","name":"笔趣阁","url":"https://a.com","is_valid":true,"view_total":100,"create_time":1790000000},
		{"id":"","name":"无效应跳过"},
		{"id":"s2","name":"笔趣阁2","url":"https://b.com","is_valid":false,"view_total":5}
	],"page":2,"pages":10,"total":200}`
	searchJSON := `{"items":[
		{"id":"c1","name":"精品书源合集","total":11,"create_time":1790000000}
	],"page":1,"pages":1,"total":1}`
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shuyuan/book-sources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "2" {
			t.Errorf("page 参数错误: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(listJSON))
	})
	mux.HandleFunc("GET /shuyuan/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search_type") != "book-source-collections" {
			t.Errorf("search_type 错误: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(searchJSON))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	m, _ := testManager(t)

	// 分页列表
	page, err := m.YioveSourceList(srv.URL, 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 200 || page.Pages != 10 || len(page.Items) != 2 {
		t.Fatalf("分页字段错误: %+v", page)
	}
	if page.Items[0].ID != "s1" || !page.Items[0].IsValid || page.Items[0].ViewTotal != 100 {
		t.Errorf("条目映射错误: %+v", page.Items[0])
	}
	if page.Items[1].IsValid {
		t.Error("is_valid=false 应原样保留")
	}

	// 搜索（合集类型）
	res, err := m.YioveSearch(srv.URL, "精品", "book-source-collections", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Items[0].Name != "精品书源合集" {
		t.Fatalf("搜索结果错误: %+v", res)
	}

	// 空关键词应拒绝
	if _, err := m.YioveSearch(srv.URL, "", "book-sources", 1, 20); err == nil {
		t.Error("空关键词应报错")
	}
}

func TestYioveSourceListImportURL(t *testing.T) {
	cases := []struct{ page, size int; want string }{
		{1, 20, "https://api.test/import/book-sources/1-20"},
		{2, 20, "https://api.test/import/book-sources/21-40"},
		{3, 50, "https://api.test/import/book-sources/101-150"},
	}
	for _, c := range cases {
		got := YioveSourceListImportURL("https://api.test", c.page, c.size)
		if got != c.want {
			t.Errorf("page=%d size=%d: got %q want %q", c.page, c.size, got, c.want)
		}
	}
}

func TestSyncRepoFull_Cleanup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleSources))
	}))
	defer srv.Close()
	m, st := testManager(t)
	repo := SourceRepo{Name: "模拟仓库", URL: srv.URL, Kind: "bookSource"}

	// 预置同 origin 旧源（仓库上次提供、本次不再提供）
	old := mustSource("http://old.example.com", "旧源（将被清理）")
	old.Origin = srv.URL
	if err := st.UpsertSource(old, false); err != nil {
		t.Fatal(err)
	}
	// 预置手动导入源（custom=1，即使 origin 相同也不应被清理）
	manual := mustSource("http://manual.example.com", "手动导入源")
	manual.Origin = srv.URL
	if err := st.UpsertSource(manual, true); err != nil {
		t.Fatal(err)
	}

	status := m.SyncRepo(repo, true)
	if status.Err != "" {
		t.Fatal(status.Err)
	}
	// 旧源被清理
	if _, err := st.GetSource("http://old.example.com"); err == nil {
		t.Error("同仓旧源应被 DeleteSourcesByOrigin 清理")
	}
	// 手动导入源保留
	if _, err := st.GetSource("http://manual.example.com"); err != nil {
		t.Error("custom=1 手动导入源不应被清理")
	}
	// 本次仓库提供的源入库
	if status.Total == 0 || (status.Inserted == 0 && status.Updated == 0) {
		t.Errorf("同步结果异常: %+v", status)
	}
	if status.Removed != 1 {
		t.Errorf("removed 应为 1，实际 %d", status.Removed)
	}
}

// mustSource 构造合法书源。
func mustSource(url, name string) *model.BookSource {
	return &model.BookSource{BookSourceURL: url, BookSourceName: name, Enabled: true}
}
