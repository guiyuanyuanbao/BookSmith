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
	// JSON 结构开头却解析失败 → 不再伪装成假 URL 分类（历史 bug：
	// GSON 宽松 JSON 击穿解析器后被兜底成「全部分类」，点击必炸）
	broken := `[{'title':'x','url':'/a'},` // 截断的非法 JSON
	if kinds := AnalyzeExploreKinds(broken, "http://example.com", &mockSourceRef{}, nil, nil); kinds != nil {
		t.Fatalf("坏 JSON 不应产生假 URL 分类: %+v", kinds)
	}
}

// TestAnalyzeExploreKinds_GSONLenient 海棠/废文网形：单引号值 + 全裸键 +
// 裸 style 子键（GSON lenient 语法，encoding/json 严格拒绝）。
func TestAnalyzeExploreKinds_GSONLenient(t *testing.T) {
	rule := "[{title:'我的书柜',url:'/?act=managemybookcase&pages=1',style:{layout_flexGrow: 1,layout_flexBasisPercent:1}},\n" +
		"{title:'百合分类',url:'/?act=searchlist&selbooktype=all&page=1',style:{layout_flexGrow: 1}},\n" +
		"{title:'已完结',url:'/?act=searchlist&selbookpoststats=b&page=1',style:{layout_flexGrow: 1}}]"
	kinds := AnalyzeExploreKinds(rule, "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds) != 3 {
		t.Fatalf("GSON 宽松 JSON 应解析 3 个分类，实际 %d: %+v", len(kinds), kinds)
	}
	if kinds[0].Title != "我的书柜" || kinds[0].URL != "/?act=managemybookcase&pages=1" {
		t.Fatalf("首分类错误: %+v", kinds[0])
	}
}

// TestAnalyzeExploreKinds_LenientMixed 青花鱼形：首个严格双引号项 url 为空
// （应过滤）+ 其余单引号裸键项（应解析）。
func TestAnalyzeExploreKinds_LenientMixed(t *testing.T) {
	rule := `[{"title":"分类","url":""},{title:'现代',url:'/forum.php?mod=forumdisplay&fid=4&page=1',style:{layout_flexgrow: 1}},` +
		"{title:'古风',url:'/forum.php?fid=2&page=1',style:{layout_flexgrow: 1}}]"
	kinds := AnalyzeExploreKinds(rule, "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds) != 2 {
		t.Fatalf("url 为空项应过滤，实际 %d: %+v", len(kinds), kinds)
	}
	if kinds[0].Title != "现代" {
		t.Fatalf("首分类错误: %+v", kinds[0])
	}
}

// TestAnalyzeExploreKinds_TrailingComma 尾逗号容忍（legado 源常见笔误）。
func TestAnalyzeExploreKinds_TrailingComma(t *testing.T) {
	rule := `[{"title":"玄幻","url":"/xh/{{page}}.html"},{"title":"都市","url":"/ds/{{page}}.html"},]`
	kinds := AnalyzeExploreKinds(rule, "http://example.com", &mockSourceRef{}, nil, nil)
	if len(kinds) != 2 {
		t.Fatalf("尾逗号应容忍，实际 %d: %+v", len(kinds), kinds)
	}
}

// TestQuoteBareJSONKeys_SkipsStringContents 裸键补引号不得误改字符串值内部
//（如 URL 值里偶然出现的 ",word:" 形态）。
func TestQuoteBareJSONKeys_SkipsStringContents(t *testing.T) {
	in := `{"url":"/a?b=1,c:d,e:f","title":"含,冒号:的值"}`
	out := quoteBareJSONKeys(in)
	if out != in {
		t.Fatalf("字符串内容不应被改动:\n in:  %s\n out: %s", in, out)
	}
	// 裸键在字符串外被正确补引号
	in2 := `{title:'x',url:'/y'}`
	out2 := quoteBareJSONKeys(strings.ReplaceAll(in2, "'", `"`))
	if out2 != `{"title":"x","url":"/y"}` {
		t.Fatalf("裸键补引号错误: %s", out2)
	}
}
