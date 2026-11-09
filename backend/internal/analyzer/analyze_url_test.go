package analyzer

import (
	"strings"
	"testing"
)

// mockJSEngine 支持 {{key}} 等绑定的测试引擎。
type mockJSEngine struct{}

func (m *mockJSEngine) Eval(script string, bindings map[string]any) (any, error) {
	// 模拟书源常见 JS：key/baseUrl 引用与简单字符串拼接
	// 字符串字面量直接求值
	trimmed := strings.TrimSpace(script)
	if strings.HasPrefix(trimmed, "'") && strings.HasSuffix(trimmed, "'") && len(trimmed) >= 2 {
		return trimmed[1 : len(trimmed)-1], nil
	}
	if strings.Contains(script, "baseUrl") {
		if b, ok := bindings["baseUrl"].(string); ok {
			// 处理 baseUrl + 'xxx' 拼接
			idx := strings.Index(script, "'")
			if idx >= 0 {
				end := strings.LastIndex(script, "'")
				if end > idx {
					return b + script[idx+1:end], nil
				}
			}
			return b, nil
		}
	}
	if v, ok := bindings["key"]; ok && strings.Contains(script, "key") {
		return v, nil
	}
	return "", nil
}

func TestAnalyzeUrlBasic(t *testing.T) {
	au, err := NewAnalyzeUrl("https://a.com/search?q={{key}}", "斗破", 1, "", nil, &mockJSEngine{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 查询串中文按 RFC3986 编码（legado encodeParams 语义）
	if !strings.HasPrefix(au.URL, "https://a.com/search?q=") {
		t.Errorf("url = %q", au.URL)
	}
	if au.Method != "GET" {
		t.Errorf("method = %s", au.Method)
	}
}

func TestAnalyzeUrlPageArray(t *testing.T) {
	// <index,{{page}}> 惯用法：page=1 取 "index"，page=2 取 "2"
	au, _ := NewAnalyzeUrl("https://a.com/<index,{{page}}>.html", "k", 1, "", nil, &mockJSEngine{}, nil, nil, nil)
	if au.URL != "https://a.com/index.html" {
		t.Errorf("page1 url = %q", au.URL)
	}
	au, _ = NewAnalyzeUrl("https://a.com/<index,{{page}}>.html", "k", 3, "", nil, &mockJSEngine{}, nil, nil, nil)
	if au.URL != "https://a.com/3.html" {
		t.Errorf("page3 url = %q", au.URL)
	}
	// 越界取最后一项
	au, _ = NewAnalyzeUrl("https://a.com/<a,b>.html", "k", 5, "", nil, &mockJSEngine{}, nil, nil, nil)
	if au.URL != "https://a.com/b.html" {
		t.Errorf("overflow url = %q", au.URL)
	}
}

func TestAnalyzeUrlOptions(t *testing.T) {
	rule := `https://a.com/s,{"method":"POST","body":{"keyword":"{{key}}","page":"{{page-1}}"},"charset":"gbk","headers":{"Referer":"https://a.com"}}`
	au, err := NewAnalyzeUrl(rule, "斗破", 2, "", nil, &mockJSEngine{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if au.Method != "POST" {
		t.Errorf("method = %s", au.Method)
	}
	if au.Charset != "gbk" {
		t.Errorf("charset = %s", au.Charset)
	}
	if au.Headers["Referer"] != "https://a.com" {
		t.Errorf("headers = %v", au.Headers)
	}
	if !strings.Contains(au.Body, "斗破") {
		t.Errorf("body = %q", au.Body)
	}
	// {{page-1}} 算术求值
	if !strings.Contains(au.Body, `"page":"1"`) && !strings.Contains(au.Body, `"page":1`) {
		t.Errorf("page arith = %q", au.Body)
	}
	// JSON body 保持 JSON（legado 语义：JSON body → JSON POST）
	req := au.BuildRequest(nil)
	if req.Method != "POST" {
		t.Errorf("req method = %s", req.Method)
	}
	if req.Headers["Content-Type"] != "application/json; charset=UTF-8" {
		t.Errorf("req content-type = %q", req.Headers["Content-Type"])
	}
	if !strings.Contains(req.Body, "keyword") {
		t.Errorf("req body = %q", req.Body)
	}
}

func TestAnalyzeUrlJsAndResult(t *testing.T) {
	// @result 占位与 JS 拼接
	rule := `https://a.com/api<js>'search'</js>.html?key={{key}}`
	au, err := NewAnalyzeUrl(rule, "剑", 1, "", nil, &mockJSEngine{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(au.URL, "search") {
		t.Errorf("js url = %q", au.URL)
	}
	if !strings.Contains(au.URL, "key=") {
		t.Errorf("key url = %q", au.URL)
	}
}

func TestAnalyzeUrlSearchKeyShim(t *testing.T) {
	// 兼容垫片：searchKey/searchPage 字面替换
	au, err := NewAnalyzeUrl("https://a.com/search?searchKey=searchKey&pn=searchPage", "天", 4, "", nil, &mockJSEngine{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(au.URL, "pn=4") {
		t.Errorf("searchPage = %q", au.URL)
	}
	if strings.Contains(au.URL, "searchKey=searchKey") {
		t.Errorf("searchKey not replaced: %q", au.URL)
	}
}

func TestAnalyzeUrlAbsoluteBase(t *testing.T) {
	au, _ := NewAnalyzeUrl("/book/1.html", "k", 1, "https://a.com/dir/", nil, &mockJSEngine{}, nil, nil, nil)
	if au.URL != "https://a.com/book/1.html" {
		t.Errorf("abs url = %q", au.URL)
	}
}

func TestAnalyzeUrlJsOptionHook(t *testing.T) {
	// options 内 js 钩子覆写 URL
	rule := `https://a.com/old,{"js":"baseUrl + 'new'"}`
	au, _ := NewAnalyzeUrl(rule, "k", 1, "https://a.com/", nil, &mockJSEngine{}, nil, nil, nil)
	if au.URL != "https://a.com/new" {
		t.Errorf("js hook url = %q", au.URL)
	}
}

func TestSplitURLOptions(t *testing.T) {
	m, o := splitURLOptions(`https://a.com/x , {"method":"POST"}`)
	if m != "https://a.com/x" {
		t.Errorf("main = %q", m)
	}
	if o != `{"method":"POST"}` {
		t.Errorf("options = %q", o)
	}
	m, o = splitURLOptions("https://a.com/nojson")
	if m != "https://a.com/nojson" || o != "" {
		t.Errorf("m=%q o=%q", m, o)
	}
}
