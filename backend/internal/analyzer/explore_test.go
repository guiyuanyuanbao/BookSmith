package analyzer

import (
	"strings"
	"testing"
)

// mockSourceRef / mockCookies 测试桩。
type mockSourceRef struct{}

func (m *mockSourceRef) GetKey() string            { return "http://test" }
func (m *mockSourceRef) GetName() string           { return "测试源" }
func (m *mockSourceRef) GetVariable() string       { return "" }
func (m *mockSourceRef) GetHeader() string         { return "" }
func (m *mockSourceRef) GetLoginUrl() string       { return "" }
func (m *mockSourceRef) GetJSLib() string          { return "" }
func (m *mockSourceRef) GetSearchURL() string      { return "" }
func (m *mockSourceRef) GetLoginCheckJS() string   { return "" }
func (m *mockSourceRef) GetConcurrentRate() string { return "" }
func (m *mockSourceRef) GetBookSourceType() int    { return 0 }

func TestAnalyzeExploreKinds_LineForm(t *testing.T) {
	// legado 标准多行 "标题::URL" 形制（种田小说/全本同人/晋江文学城实测）
	rule := "完本::/quanbenwanjie.html\n玄幻::/xiaoshuofenlei/xuanhuan.html\n修真::/xiaoshuofenlei/xiuzhen.html"
	kinds := AnalyzeExploreKinds(rule, "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds) != 3 {
		t.Fatalf("应解析 3 个分类，实际 %d: %+v", len(kinds), kinds)
	}
	if kinds[0].Title != "完本" || kinds[0].URL != "/quanbenwanjie.html" {
		t.Fatalf("首分类错误: %+v", kinds[0])
	}
}

func TestAnalyzeExploreKinds_EmptyURLLines(t *testing.T) {
	// 含分组标题行（url 为空，如追书神器"♂::"）与空行
	rule := "♂::\n畅销榜::http://a.com/rank1?page={{page}}\n\n热度榜::http://a.com/rank2?page={{page}}"
	kinds := AnalyzeExploreKinds(rule, "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds) != 2 {
		t.Fatalf("空 URL 行应跳过，实际 %d: %+v", len(kinds), kinds)
	}
	if kinds[0].Title != "畅销榜" {
		t.Fatalf("首分类错误: %+v", kinds[0])
	}
}

func TestAnalyzeExploreKinds_JSONForm(t *testing.T) {
	// JSON 数组形态（含 url 为空的分组标题行：灯读文学实测）
	rule := `[{"title":"男生频道","url":""},{"title":"玄幻","url":"/book/index?category_id=1&page={{page}}"},{"title":"都市","url":"/book/index?category_id=2&page={{page}}"}]`
	kinds := AnalyzeExploreKinds(rule, "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds) != 2 {
		t.Fatalf("url 为空的分组标题应过滤，实际 %d: %+v", len(kinds), kinds)
	}
	if kinds[0].Title != "玄幻" {
		t.Fatalf("首分类错误: %+v", kinds[0])
	}
}

// TestAnalyzeExploreKinds_AndSeparator legado 分隔语义 "(&&|\n)+"：除换行还支持 && 分隔多分类。
func TestAnalyzeExploreKinds_AndSeparator(t *testing.T) {
	rule := "玄幻::/xh/{{page}}.html&&都市::/ds/{{page}}.html&&完本::/wb/"
	kinds := AnalyzeExploreKinds(rule, "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds) != 3 {
		t.Fatalf("&& 分隔应解析 3 个分类，实际 %d: %+v", len(kinds), kinds)
	}
	if kinds[0].Title != "玄幻" || kinds[2].Title != "完本" {
		t.Fatalf("分类错误: %+v", kinds)
	}
	// 混合分隔：&& 与换行混用
	mixed := "玄幻::/xh/\n都市::/ds/&&科幻::/kh/"
	kinds2 := AnalyzeExploreKinds(mixed, "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds2) != 3 {
		t.Fatalf("混合分隔应解析 3 个分类，实际 %d", len(kinds2))
	}
}

func TestAnalyzeExploreKinds_PureURLFallback(t *testing.T) {
	// 纯 URL（无分类结构）→ 单分类兜底
	kinds := AnalyzeExploreKinds("/latest/page/{{page}}", "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds) != 1 || kinds[0].Title != "全部分类" {
		t.Fatalf("纯 URL 兜底错误: %+v", kinds)
	}
	if !strings.Contains(kinds[0].URL, "/latest/") {
		t.Fatalf("URL 应保留: %+v", kinds[0])
	}
}
