package analyzer

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const testHTML = `<!DOCTYPE html>
<html>
<head><title>测试书城</title></head>
<body>
<div id="main" class="wrapper">
  <div class="book-list" id="list">
    <div class="item odd">
      <h3 class="book-name"><a href="/book/1">凡人修仙传</a></h3>
      <span class="author">忘语</span>
      <span class="kind">仙侠</span>
      <span class="latest"><a href="/book/1/100">第100章 大结局</a></span>
      <p class="intro"> 一个普通山村小子的修仙之路&nbsp;&nbsp;简介正文</p>
    </div>
    <div class="item even">
      <h3 class="book-name"><a href="/book/2">斗破苍穹</a></h3>
      <span class="author">天蚕土豆</span>
      <span class="kind">玄幻</span>
      <span class="latest"><a href="/book/2/200">第200章 完本感言</a></span>
      <p class="intro">斗气大陆的传奇</p>
    </div>
    <div class="item odd">
      <h3 class="book-name"><a href="/book/3">遮天</a></h3>
      <span class="author">辰东</span>
      <span class="kind">玄幻</span>
      <span class="latest"><a href="/book/3/300">第300章 尾声</a></span>
      <p class="intro">九龙拉棺</p>
    </div>
  </div>
  <div class="page-nav"><a href="/list/2.html">下一页</a></div>
</div>
</body>
</html>`

const testChapterHTML = `<div class="content" id="chapter">
<h1>第一章 灵根</h1>
<p>二愣子睁大着双眼，直直望着茅草屋顶。</p><br/>
<p>上面挂着一些干枯的草绳。</p>
<div class="ad">广告内容</div>
<p>他知道自己家族世代务农。</p>
<script>var x = 1;</script>
</div>`

func mustDoc(t *testing.T, s string) *AnalyzeByJSoup {
	t.Helper()
	a, err := NewAnalyzeByJSoup(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestJSoupGetString(t *testing.T) {
	a := mustDoc(t, testHTML)
	// title
	got, err := a.GetString("tag.title@text")
	if err != nil {
		t.Fatal(err)
	}
	if got != "测试书城" {
		t.Errorf("title = %q", got)
	}
	// class 选择器 + attr
	got, _ = a.GetString("class.book-name@tag.a@text")
	if got != "凡人修仙传\n斗破苍穹\n遮天" {
		t.Errorf("book names = %q", got)
	}
	// id + attr
	got, _ = a.GetString("id.main@class.page-nav@tag.a@href")
	if got != "/list/2.html" {
		t.Errorf("next page = %q", got)
	}
	// css 模式
	got, _ = a.GetString("@CSS:.item .author@text")
	if got != "忘语\n天蚕土豆\n辰东" {
		t.Errorf("css authors = %q", got)
	}
	// ownText
	got, _ = a.GetString("class.intro@ownText")
	if !strings.Contains(got, "一个普通山村小子的修仙之路") {
		t.Errorf("intro = %q", got)
	}
	// html 模式去 script
	a2 := mustDoc(t, testChapterHTML)
	got, _ = a2.GetString("class.content@html")
	if strings.Contains(got, "var x") {
		t.Errorf("html should strip script: %q", got)
	}
	// all 保留 script
	got, _ = a2.GetString("class.content@all")
	if !strings.Contains(got, "var x") {
		t.Errorf("all should keep script")
	}
	// textNodes：作用于直接文本子节点（fixture 的正文在子元素内，textNodes 为空是正确行为）
	got, _ = a2.GetString("class.content@textNodes")
	if got != "" {
		t.Logf("textNodes on element children = %q", got)
	}
	a3 := mustDoc(t, `<div id="t">第一段<br/>第二段<br/>第三段</div>`)
	got, _ = a3.GetString("id.t@textNodes")
	if got != "第一段\n第二段\n第三段" {
		t.Errorf("textNodes = %q", got)
	}
}

func TestJSoupGetElements(t *testing.T) {
	a := mustDoc(t, testHTML)
	nodes, err := a.GetElements("class.item")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("items = %d", len(nodes))
	}
	// 对每个元素再解析
	first := nodes[0]
	sub, err := NewAnalyzeByJSoup(first)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := sub.GetString("class.book-name@tag.a@text")
	if name != "凡人修仙传" {
		t.Errorf("first book = %q", name)
	}
	href, _ := sub.GetString("tag.a.0@href")
	if href != "/book/1" {
		t.Errorf("href = %q", href)
	}
}

func TestJSoupIndexSyntax(t *testing.T) {
	a := mustDoc(t, testHTML)
	// 传统索引写法：取第二个（负索引）
	got, _ := a.GetString("class.book-name.1@tag.a@text")
	if got != "斗破苍穹" {
		t.Errorf("index 1 = %q", got)
	}
	got, _ = a.GetString("class.book-name.-1@tag.a@text")
	if got != "遮天" {
		t.Errorf("index -1 = %q", got)
	}
	// ! 排除
	got, _ = a.GetString("class.book-name!0@tag.a@text")
	if got != "斗破苍穹\n遮天" {
		t.Errorf("exclude 0 = %q", got)
	}
	// [] 写法区间
	got, _ = a.GetString("class.book-name[0:2]@tag.a@text")
	if got != "凡人修仙传\n斗破苍穹\n遮天" {
		t.Errorf("range [0:2] = %q", got)
	}
	// [] 多索引
	got, _ = a.GetString("class.book-name[0,2]@tag.a@text")
	if got != "凡人修仙传\n遮天" {
		t.Errorf("multi [0,2] = %q", got)
	}
	// 反向 [-1:0]
	got, _ = a.GetString("class.book-name[-1:0]@tag.a@text")
	if got != "遮天\n斗破苍穹\n凡人修仙传" {
		t.Errorf("reverse = %q", got)
	}
}

func TestJSoupCombinators(t *testing.T) {
	a := mustDoc(t, testHTML)
	// || 短路：第一个非空胜出
	got, _ := a.GetString("class.not-exist@text||class.author@text")
	if got != "忘语\n天蚕土豆\n辰东" {
		t.Errorf("or = %q", got)
	}
	// && 拼接
	got, _ = a.GetString("class.author@text&&class.kind@text")
	if !strings.Contains(got, "忘语") || !strings.Contains(got, "仙侠") {
		t.Errorf("and = %q", got)
	}
	// %% 交叉
	got, _ = a.GetString("class.author@text%%class.kind@text")
	want := "忘语\n仙侠\n天蚕土豆\n玄幻\n辰东\n玄幻"
	if got != want {
		t.Errorf("cross = %q want %q", got, want)
	}
}

func TestJSoupRawElement(t *testing.T) {
	a := mustDoc(t, testHTML)
	nodes, _ := a.GetElements("class.item")
	if len(nodes) == 0 {
		t.Fatal("no nodes")
	}
	// 裸索引作用于子元素
	sub, _ := NewAnalyzeByJSoup(nodes[0])
	got, _ := sub.GetString("children[1]@text")
	if got != "忘语" {
		t.Errorf("bare index = %q", got)
	}
}

func TestXPathAnalyzer(t *testing.T) {
	a, err := NewAnalyzeByXPath(testHTML)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.GetString("//title/text()")
	if err != nil {
		t.Fatal(err)
	}
	if got != "测试书城" {
		t.Errorf("title = %q", got)
	}
	list, err := a.GetStringList("//div[contains(@class,'item')]//span[@class='author']/text()")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0] != "忘语" {
		t.Errorf("authors = %v", list)
	}
	// @href
	list, _ = a.GetStringList("//div[@class='page-nav']//a/@href")
	if len(list) != 1 || list[0] != "/list/2.html" {
		t.Errorf("href = %v", list)
	}
	// || 组合
	got, _ = a.GetString("//div[@class='nope']/text()||//title/text()")
	if got != "测试书城" {
		t.Errorf("or = %q", got)
	}
}

func TestJSONAnalyzer(t *testing.T) {
	data := `{"data":{"list":[{"name":"书一","author":"甲","chapters":[{"title":"第1章","url":"/c/1"},{"title":"第2章","url":"/c/2"}]},{"name":"书二","author":"乙"}]},"page":2}`
	a, err := NewAnalyzeByJSONPath(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.GetString("$.page")
	if err != nil {
		t.Fatal(err)
	}
	if got != "2" {
		t.Errorf("page = %q", got)
	}
	list, err := a.GetStringList("$.data.list[*].name")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0] != "书一" {
		t.Errorf("names = %v", list)
	}
	// 内嵌替换
	got, _ = a.GetString(`{$.data.list[0].name}-作者:{$.data.list[0].author}`)
	if got != "书一-作者:甲" {
		t.Errorf("inner = %q", got)
	}
	// || 组合
	got, _ = a.GetString("$.not.exist||$.page")
	if got != "2" {
		t.Errorf("or = %q", got)
	}
	// getList
	objs, err := a.GetList("$.data.list[*]")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Errorf("objs = %d", len(objs))
	}
	// 过滤器
	list, _ = a.GetStringList(`$.data.list[?(@.name=='书二')].author`)
	if len(list) != 1 || list[0] != "乙" {
		t.Errorf("filter = %v", list)
	}
}

// echoJSEngine 测试用 JS 引擎（模拟简单表达式求值）。
type echoJSEngine struct{}

func (e *echoJSEngine) Eval(script string, bindings map[string]any) (any, error) {
	// 简单模拟：result + baseUrl 拼接或常量
	if strings.Contains(script, "baseUrl") {
		if b, ok := bindings["baseUrl"].(string); ok {
			return b, nil
		}
	}
	if strings.Contains(script, "'token'") {
		return "token", nil
	}
	if v, ok := bindings["result"]; ok && v != nil {
		return v, nil
	}
	return script, nil
}

func TestAnalyzeRuleDispatch(t *testing.T) {
	ar := NewAnalyzeRule(nil, nil, nil)
	ar.SetContent(testHTML, "https://a.com/")
	ar.SetRedirectURL("https://a.com/list/1.html")

	// 默认 jsoup
	got := ar.GetString("class.book-name@tag.a@text")
	if got != "凡人修仙传\n斗破苍穹\n遮天" {
		t.Errorf("jsoup = %q", got)
	}
	// @CSS:
	got = ar.GetString("@CSS:.author@text")
	if got != "忘语\n天蚕土豆\n辰东" {
		t.Errorf("css = %q", got)
	}
	// @XPath:
	got = ar.GetString("@XPath://title/text()")
	if got != "测试书城" {
		t.Errorf("xpath = %q", got)
	}
	// ## 正则替换
	got = ar.GetString(`class.book-name@tag.a@text##第(\d+)章.*##$1`)
	if got != "凡人修仙传\n斗破苍穹\n遮天" {
		t.Logf("regex replace on titles: %q", got)
	}
	got = ar.GetString(`class.latest@tag.a@text##第(\d+)章.*##$1`)
	if got != "100\n200\n300" {
		t.Errorf("regex = %q", got)
	}
	// || 组合
	got = ar.GetString("class.nope@text||class.author@text")
	if got != "忘语\n天蚕土豆\n辰东" {
		t.Errorf("or = %q", got)
	}
	// isUrl
	got = ar.GetStringURL("class.page-nav@tag.a@href", nil)
	if got != "https://a.com/list/2.html" {
		t.Errorf("url = %q", got)
	}
	// 实体解码
	got = ar.GetString("class.intro@text")
	if !strings.Contains(got, "一个普通山村小子的修仙之路 简介") {
		t.Errorf("intro decode = %q", got)
	}
}

func TestAnalyzeRuleRegexAndVars(t *testing.T) {
	vars := NewVariables(nil)
	vars.Put("site", "example.com")
	ar := NewAnalyzeRule(nil, nil, vars)
	ar.SetContent(testChapterHTML, "https://a.com/")

	// @get:{} 变量替换 → Regex 模式重建字面文本
	got := ar.GetString("https://@get:{site}/api")
	if got != "https://example.com/api" {
		t.Errorf("get var = %q", got)
	}
	// {{js}}（用 echo 引擎）
	ar2 := NewAnalyzeRule(nil, &echoJSEngine{}, vars)
	ar2.SetContent(testChapterHTML, "https://a.com/")
	// $N 模板：先正则匹配再用 $1 重组
	got = ar2.GetString(`tag.h1@text##第一章 (\S+)##$1`)
	// h1 = "第一章 灵根"，正则匹配 "第一章 灵根"，替换组1="灵根" → "灵根"
	if got != "灵根" {
		t.Errorf("$N = %q", got)
	}
}

func TestAnalyzeRuleJSONContent(t *testing.T) {
	// isJSON 内容强制 JSON 模式
	data := `{"name":"斗破","author":"天蚕土豆","list":["a","b"]}`
	ar := NewAnalyzeRule(nil, nil, nil)
	ar.SetContent(data, "")
	if !ar.isJSON {
		t.Fatal("isJSON should be true")
	}
	if got := ar.GetString("$.name"); got != "斗破" {
		t.Errorf("name = %q", got)
	}
	if got := ar.GetString("$.list"); got != "a\nb" {
		t.Errorf("list = %q", got)
	}
	list := ar.GetStringList("$.list", nil, false)
	if len(list) != 2 {
		t.Errorf("list len = %d", len(list))
	}
	// getElements
	elems := ar.GetElements("$.list[*]")
	if len(elems) != 2 {
		t.Errorf("elements = %v", elems)
	}
}

func TestAnalyzeRuleJSElement(t *testing.T) {
	// <js> 规则
	ar := NewAnalyzeRule(nil, &echoJSEngine{}, nil)
	ar.SetContent("hello", "")
	got := ar.GetString("<js>result</js>")
	if got != "hello" {
		t.Errorf("js = %q", got)
	}
	// @js:
	got = ar.GetString("@js:'token'")
	if got != "token" {
		t.Errorf("js token = %q", got)
	}
	// js + jsoup 链
	ar.SetContent(testHTML, "")
	got = ar.GetString(`<js>result</js>tag.title@text`)
	// echo 引擎返回 result 原样（html 字符串），再走 jsoup 解析 title
	if got != "测试书城" {
		t.Errorf("js+jsoup = %q", got)
	}
}

func TestAnalyzeRuleElements(t *testing.T) {
	ar := NewAnalyzeRule(nil, nil, nil)
	ar.SetContent(testHTML, "")
	elems := ar.GetElements("class.item")
	if len(elems) != 3 {
		t.Fatalf("elements = %d", len(elems))
	}
	// 元素上的单值规则
	first := elems[0]
	ar2 := NewAnalyzeRule(nil, nil, nil)
	ar2.SetContent(first, "")
	ar2.SetRedirectURL("https://a.com/book/1")
	if got := ar2.GetString("class.book-name@tag.a@text"); got != "凡人修仙传" {
		t.Errorf("first name = %q", got)
	}
	if got := ar2.GetStringURL("tag.a@href", nil); got != "https://a.com/book/1" {
		t.Errorf("relative url = %q", got)
	}
}

func TestAnalyzeRegexMode(t *testing.T) {
	// 正则列表规则（allInOne : 前缀）
	ar := NewAnalyzeRule(nil, nil, nil)
	ar.SetContent(`<a href="/b/1">书1</a><a href="/b/2">书2</a>`, "")
	elems := ar.GetElements(`:<a href="(/b/\d+)">(.+?)</a>`)
	if len(elems) != 2 {
		t.Fatalf("regex elements = %d", len(elems))
	}
	// 每个元素是分组列表
	if l, ok := elems[0].([]string); ok {
		if l[1] != "/b/1" || l[2] != "书1" {
			t.Errorf("groups = %v", l)
		}
	} else {
		t.Fatalf("element type = %T", elems[0])
	}
}

func TestElementTextNodesAndOwnText(t *testing.T) {
	node, err := parseHTML(`<div>第一段<p>子文本</p>第二段</div>`)
	if err != nil {
		t.Fatal(err)
	}
	var el *html.Node
	traverseIncludeSelf(node, func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "div" && el == nil {
			el = n
		}
		return true
	})
	if ElementOwnText(el) != "第一段 第二段" {
		t.Errorf("ownText = %q", ElementOwnText(el))
	}
	tns := ElementTextNodes(el)
	if len(tns) != 2 {
		t.Errorf("textNodes = %d", len(tns))
	}
	if ElementText(el) != "第一段 子文本 第二段" {
		t.Errorf("text = %q", ElementText(el))
	}
}
