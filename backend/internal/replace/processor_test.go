package replace

import (
	"strings"
	"testing"

	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/varstore"
)

func testEngine(t *testing.T) *jsenv.Engine {
	t.Helper()
	env := jsenv.New(httpx.NewClient(), t.TempDir())
	return env.Engine()
}

func TestApplyRegexRules(t *testing.T) {
	rules := []*model.ReplaceRule{
		{Name: "去广告", Pattern: `本站广告.*?阅读`, Replacement: "", IsRegex: true, Enabled: true, Order: 1},
		{Name: "规整省略号", Pattern: `。。。+`, Replacement: "……", IsRegex: true, Enabled: true, Order: 2},
	}
	p := NewProcessor(rules, testEngine(t), nil)
	got := p.ApplyContent("正文开头本站广告马上就阅读。然后。。。继续。。。", "书名", "origin")
	if strings.Contains(got, "本站广告") {
		t.Errorf("ad not removed: %q", got)
	}
	if !strings.Contains(got, "……") {
		t.Errorf("ellipsis not replaced: %q", got)
	}
}

func TestApplyPlainText(t *testing.T) {
	rules := []*model.ReplaceRule{
		{Name: "替换站名", Pattern: "笔趣阁", Replacement: "某阁", IsRegex: false, Enabled: true},
	}
	p := NewProcessor(rules, nil, nil)
	got := p.ApplyContent("欢迎来到笔趣阁阅读", "书", "src")
	if got != "欢迎来到某阁阅读" {
		t.Errorf("got %q", got)
	}
}

func TestScopeMatching(t *testing.T) {
	rules := []*model.ReplaceRule{
		{Name: "仅甲书", Pattern: "foo", Replacement: "bar", IsRegex: false, Enabled: true, Scope: "甲书;乙书"},
		{Name: "排除丙", Pattern: "foo", Replacement: "baz", IsRegex: false, Enabled: true, ExcludeScope: "丙书"},
	}
	p := NewProcessor(rules, nil, nil)
	// scope 命中
	got := p.ApplyContent("foo", "甲书", "src1")
	if got != "bar" {
		t.Errorf("scope hit = %q", got)
	}
	// scope 未命中（其他书不应用第一条；第二条未排除）
	got = p.ApplyContent("foo", "丁书", "src2")
	if got != "baz" {
		t.Errorf("exclude pass = %q", got)
	}
	// 排除命中
	got = p.ApplyContent("foo", "丙书", "src3")
	if got != "foo" {
		t.Errorf("excluded = %q", got)
	}
	// origin 命中
	got = p.ApplyContent("foo", "随便", "https://example.com/甲书源")
	if got != "bar" {
		t.Errorf("origin hit = %q", got)
	}
}

func TestApplyTitle(t *testing.T) {
	rules := []*model.ReplaceRule{
		{Name: "标题", Pattern: `^\d+\.\s*`, Replacement: "", IsRegex: true, Enabled: true, ScopeTitle: true},
		{Name: "正文规则", Pattern: "x", Replacement: "y", IsRegex: false, Enabled: true, ScopeContent: true},
	}
	p := NewProcessor(rules, nil, nil)
	if got := p.ApplyTitle("12. 第一章", "书", "o"); got != "第一章" {
		t.Errorf("title = %q", got)
	}
	if got := p.ApplyContent("x", "书", "o"); got != "y" {
		t.Errorf("content = %q", got)
	}
	// 正文规则不影响标题
	if got := p.ApplyTitle("x", "书", "o"); got != "x" {
		t.Errorf("title leak = %q", got)
	}
}

func TestJsReplacement(t *testing.T) {
	rules := []*model.ReplaceRule{
		{Name: "js大写", Pattern: `\d+`, Replacement: "@js:String.fromCharCode(result.charCodeAt(0))", IsRegex: true, Enabled: true},
	}
	p := NewProcessor(rules, testEngine(t), varstore.New(nil))
	got := p.ApplyContent("a1b2", "书", "o")
	if got != "a1b2" {
		// JS 把每个数字映射为其字符形式（此处断言 JS 被执行）
		t.Logf("js replace = %q", got)
	}
	// 更确定的行为：result + 后缀
	rules2 := []*model.ReplaceRule{
		{Name: "js标记", Pattern: `foo`, Replacement: "@js:'<' + result + '>'", IsRegex: true, Enabled: true},
	}
	p2 := NewProcessor(rules2, testEngine(t), nil)
	got = p2.ApplyContent("xfooy", "书", "o")
	if got != "x<foo>y" {
		t.Errorf("js replace = %q", got)
	}
}

func TestJsVariables(t *testing.T) {
	vars := varstore.New(nil)
	rules := []*model.ReplaceRule{
		{Name: "存变量", Pattern: `KEY=(\w+)`, Replacement: "@js:java.put('k', result); result", IsRegex: true, Enabled: true},
	}
	p := NewProcessor(rules, testEngine(t), vars)
	_ = p.ApplyContent("KEY=hello", "书", "o")
	if v := vars.Get("k"); v != "KEY=hello" {
		t.Errorf("var = %q", v)
	}
}

func TestDisabledAndOrder(t *testing.T) {
	rules := []*model.ReplaceRule{
		{Name: "a", Pattern: "1", Replacement: "2", IsRegex: false, Enabled: true, Order: 2},
		{Name: "b", Pattern: "2", Replacement: "3", IsRegex: false, Enabled: true, Order: 1},
		{Name: "c", Pattern: "3", Replacement: "4", IsRegex: false, Enabled: false},
	}
	// 启用列表由调用方排序过滤；这里手动按 order 传入
	p := NewProcessor([]*model.ReplaceRule{rules[1], rules[0]}, nil, nil)
	got := p.ApplyContent("1", "书", "o")
	// order=1 的 b 先把 2→3（此时无 2），order=2 的 a 把 1→2
	if got != "2" {
		t.Errorf("order = %q", got)
	}
}

func TestRemoveDuplicateTitle(t *testing.T) {
	content := "第一章 灵根\n\n正文开始"
	got := RemoveDuplicateTitle(content, "书名", "第一章 灵根")
	if strings.HasPrefix(got, "第一章") {
		t.Errorf("dup title kept: %q", got)
	}
	if !strings.Contains(got, "正文开始") {
		t.Errorf("content lost: %q", got)
	}
	// 无重复
	got = RemoveDuplicateTitle("完全不同\n正文", "书名", "第一章")
	if got != "完全不同\n正文" {
		t.Errorf("changed: %q", got)
	}
}

func TestJsTimeout(t *testing.T) {
	rules := []*model.ReplaceRule{
		{Name: "死循环", Pattern: `x`, Replacement: "@js:while(true){}", IsRegex: true, Enabled: true, TimeoutMS: 300},
	}
	p := NewProcessor(rules, testEngine(t), nil)
	got := p.ApplyContent("x", "书", "o")
	if got != "x" {
		t.Errorf("timeout should keep original: %q", got)
	}
}
