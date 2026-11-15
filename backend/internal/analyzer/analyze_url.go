package analyzer

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"booksmith/internal/httpx"
)

// AnalyzeUrl 是 legado AnalyzeUrl.kt 的移植：解析 URL 规则并生成 HTTP 请求。
// 支持 <js></js>、{{js}}、@result 占位、<a,b,c> 页码数组、,{...} options JSON。
type AnalyzeUrl struct {
	// 最终请求参数
	Method    string
	URL       string
	Headers   map[string]string
	Body      string
	Charset   string
	Type      string
	Retry     int
	TimeoutMS int
	// WebView 相关（服务端不支持时忽略）
	WebView     bool
	WebJS       string
	SourceRegex string
	// bodyJs：响应后执行
	BodyJS string

	// 解析上下文
	source   BookSourceRef
	js       JSEngine
	page     int
	key      string
	bindings map[string]any
	vars     VariableProvider
	cookies  CookieProvider
}

// CookieProvider Cookie 读取（jsenv.CookieStore 满足）。
type CookieProvider interface {
	GetCookie(urlStr string) string
}

// NewAnalyzeUrl 创建 URL 分析器。page 从 1 开始。
func NewAnalyzeUrl(ruleURL, key string, page int, baseURL string, source BookSourceRef, js JSEngine, vars VariableProvider, cookies CookieProvider, extraBindings map[string]any) (*AnalyzeUrl, error) {
	au := &AnalyzeUrl{
		Method:   "GET",
		page:     page,
		key:      key,
		source:   source,
		js:       js,
		vars:     vars,
		cookies:  cookies,
		bindings: map[string]any{},
	}
	for k, v := range extraBindings {
		au.bindings[k] = v
	}
	au.bindings["key"] = key
	au.bindings["page"] = page
	if baseURL != "" {
		au.bindings["baseUrl"] = baseURL
	}
	if err := au.initURL(ruleURL, baseURL); err != nil {
		return nil, err
	}
	return au, nil
}

var (
	urlPagePattern = regexp.MustCompile(`<(.*?)>`)

	urlSearchKeyRe = regexp.MustCompile(`\{\{page\}\}`)
)

// resultPlaceholder <js> 结果占位符。
const resultPlaceholder = "@result"

// initUrl 解析流水线：analyzeJs → replaceKeyPageJs → analyzeUrl。
func (au *AnalyzeUrl) initURL(ruleURL, baseURL string) error {
	ruleURL = au.analyzeJsMulti(ruleURL)
	ruleURL = au.replaceKeyPageJS(ruleURL)
	return au.analyzeUrlOption(ruleURL, baseURL)
}

// analyzeJsMulti 执行 <js></js> 段，@result 占位承接上一段结果。
func (au *AnalyzeUrl) analyzeJsMulti(ruleURL string) string {
	locs := jsPattern.FindAllStringSubmatchIndex(ruleURL, -1)
	if len(locs) == 0 {
		return ruleURL
	}
	var result string
	var sb strings.Builder
	last := 0
	for _, loc := range locs {
		// JS 段
		var jsBody string
		if loc[2] >= 0 { // <js>...</js>
			jsBody = ruleURL[loc[2]:loc[3]]
		} else if loc[4] >= 0 { // @js:
			jsBody = ruleURL[loc[4]:loc[5]]
		}
		if loc[0] > last {
			text := ruleURL[last:loc[0]]
			if result != "" {
				text = strings.ReplaceAll(text, resultPlaceholder, result)
			}
			sb.WriteString(text)
			if jsBody != "" {
				result = toStringValue(au.evalJS(jsBody, result))
				sb.WriteString(result)
			}
		} else if jsBody != "" {
			result = toStringValue(au.evalJS(jsBody, result))
			sb.WriteString(result)
		}
		last = loc[1]
	}
	if last < len(ruleURL) {
		text := ruleURL[last:]
		if result != "" {
			text = strings.ReplaceAll(text, resultPlaceholder, result)
		}
		sb.WriteString(text)
	}
	return sb.String()
}

// replaceKeyPageJS 处理 {{js}} 内嵌与 <a,b,c> 页码数组。
func (au *AnalyzeUrl) replaceKeyPageJS(ruleURL string) string {
	if strings.Contains(ruleURL, "{{") && strings.Contains(ruleURL, "}}") {
		ra := NewRuleAnalyzer(ruleURL, true)
		ruleURL = ra.InnerRulePair("{{", "}}", func(jsBody string) string {
			v := au.evalJS(jsBody, nil)
			return jsResultToString(v)
		})
	}
	// 页码数组 <a,b,c>：page 为 1-based，越界取最后一项
	if strings.Contains(ruleURL, "<") && strings.Contains(ruleURL, ">") {
		ruleURL = urlPagePattern.ReplaceAllStringFunc(ruleURL, func(m string) string {
			pages := strings.Split(m[1:len(m)-1], ",")
			if len(pages) == 0 {
				return m
			}
			// 每个 page 元素内的 {{page}} 替换为当前页码（legado 惯用法 <index,{{page}}>）
			idx := au.page
			if idx < 1 {
				idx = 1
			}
			if idx <= len(pages) {
				return strings.ReplaceAll(pages[idx-1], "{{page}}", strconv.Itoa(au.page))
			}
			return strings.ReplaceAll(pages[len(pages)-1], "{{page}}", strconv.Itoa(au.page))
		})
	}
	// 兼容垫片：searchKey / searchPage 字面替换（旧官方语法，社区书源仍大量使用）
	if au.key != "" {
		ruleURL = strings.ReplaceAll(ruleURL, "searchKey", encodeQuery(au.key))
	}
	ruleURL = strings.ReplaceAll(ruleURL, "searchPage", strconv.Itoa(au.page))
	return ruleURL
}

// splitURLOptions 切分 URL 主体与 ,{options} JSON（支持逗号前后空白）。
func splitURLOptions(ruleURL string) (string, string) {
	bi := strings.Index(ruleURL, "{")
	if bi < 0 {
		return ruleURL, ""
	}
	// 从 { 向前找逗号（允许空白）
	co := -1
	for j := bi - 1; j >= 0; j-- {
		c := ruleURL[j]
		if c == ',' {
			co = j
			break
		}
		if c != ' ' && c != '	' {
			break
		}
	}
	if co < 0 {
		return ruleURL, ""
	}
	return strings.TrimSpace(ruleURL[:co]), strings.TrimSpace(ruleURL[co+1:])
}

// analyzeUrlOption 切分 URL 与 ,{options} JSON。
func (au *AnalyzeUrl) analyzeUrlOption(ruleURL, baseURL string) error {
	urlMain, optionsJSON := splitURLOptions(ruleURL)
	if baseURL != "" && urlMain != "" {
		urlMain = AbsoluteURLString(baseURL, urlMain)
	}
	au.URL = urlMain

	// query 编码处理（中文关键字 RFC3986）
	if strings.Contains(urlMain, "?") && au.key != "" {
		au.URL = encodeURLQuery(urlMain)
	}

	// data: URI
	if strings.HasPrefix(au.URL, "data:") {
		return nil
	}

	// options
	if optionsJSON != "" {
		var opts map[string]any
		b := []byte(optionsJSON)
		if err := json.Unmarshal(b, &opts); err != nil {
			// 宽松解析
			var loose map[string]any
			if err2 := jsonUnmarshalLoose(b, &loose); err2 != nil {
				return fmt.Errorf("URL options 解析失败: %w", err)
			}
			opts = loose
		}
		au.applyOptions(opts)
	}
	if au.Method == "" {
		au.Method = "GET"
	}
	return nil
}

// applyOptions 应用 UrlOption。
func (au *AnalyzeUrl) applyOptions(opts map[string]any) {
	getStr := func(key string) string {
		if v, ok := opts[key]; ok {
			return toStringAny(v)
		}
		return ""
	}
	if m := getStr("method"); m != "" {
		switch strings.ToUpper(m) {
		case "POST":
			au.Method = "POST"
		case "HEAD":
			au.Method = "HEAD"
		default:
			au.Method = "GET"
		}
	}
	if cs := getStr("charset"); cs != "" {
		au.Charset = cs
	}
	if t := getStr("type"); t != "" {
		au.Type = t
	}
	if r := getStr("retry"); r != "" {
		if n, err := strconv.Atoi(r); err == nil {
			au.Retry = n
		}
	}
	if to := getStr("timeout"); to != "" {
		if f, err := strconv.ParseFloat(to, 64); err == nil {
			au.TimeoutMS = int(f)
		}
	}
	if wv := getStr("webView"); wv != "" && wv != "false" && wv != "0" {
		au.WebView = true
	}
	if wj := getStr("webJs"); wj != "" {
		au.WebJS = wj
	}
	if sr := getStr("sourceRegex"); sr != "" {
		au.SourceRegex = sr
	}
	if bj := getStr("bodyJs"); bj != "" {
		au.BodyJS = bj
	}
	// headers
	if hv, ok := opts["headers"]; ok {
		headers := map[string]string{}
		switch h := hv.(type) {
		case string:
			headers = httpx.ParseHeaders(h)
		case map[string]any:
			for k, v := range h {
				headers[k] = toStringAny(v)
			}
		}
		if proxy, ok := headers["proxy"]; ok {
			delete(headers, "proxy")
			_ = proxy // 代理支持由客户端配置统一处理
		}
		if au.Headers == nil {
			au.Headers = headers
		} else {
			for k, v := range headers {
				au.Headers[k] = v
			}
		}
	}
	// body
	if bv, ok := opts["body"]; ok {
		switch b := bv.(type) {
		case string:
			au.Body = b
		case map[string]any:
			// 规范化 JSON body
			normalized := map[string]any{}
			for k, v := range b {
				normalized[k] = au.normalizeBodyValue(v)
			}
			if jb, err := json.Marshal(normalized); err == nil {
				au.Body = string(jb)
			}
		default:
			au.Body = toStringAny(bv)
		}
	}
	// js：URL 解析完成后执行，返回值覆写 url
	if j := getStr("js"); j != "" {
		if v := au.evalJS(j, au.URL); v != nil {
			s := jsResultToString(v)
			if s != "" {
				au.URL = s
			}
		}
	}
}

// normalizeBodyValue 规范化 body 值（{"offset":0,"page":"{{page-1}}"} 等 JS 表达式）。
func (au *AnalyzeUrl) normalizeBodyValue(v any) any {
	switch x := v.(type) {
	case string:
		// 支持 {{page-1}} 等 JS 算术
		if strings.HasPrefix(x, "{{") && strings.HasSuffix(x, "}}") {
			expr := strings.TrimSuffix(strings.TrimPrefix(x, "{{"), "}}")
			// 代入 page 变量后尝试简单算术
			expr = strings.ReplaceAll(expr, "page", strconv.Itoa(au.page))
			expr = strings.ReplaceAll(expr, "key", au.key)
			if n, err := evaluateSimpleArith(expr); err == nil {
				return n
			}
			return x
		}
		return x
	default:
		return x
	}
}

var simpleArithRe = regexp.MustCompile(`^\s*(\d+)\s*([+\-*])\s*(\d+)\s*$`)

// evaluateSimpleArith 求简单算术（避免为 body 中的 {{page-1}} 起完整 JS）。
func evaluateSimpleArith(expr string) (int, error) {
	m := simpleArithRe.FindStringSubmatch(expr)
	if m == nil {
		return 0, fmt.Errorf("not simple arith: %s", expr)
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[3])
	switch m[2] {
	case "+":
		return a + b, nil
	case "-":
		return a - b, nil
	case "*":
		return a * b, nil
	}
	return 0, fmt.Errorf("bad op")
}

// evalJS 执行 JS。
func (au *AnalyzeUrl) evalJS(jsStr string, result any) any {
	if au.js == nil {
		return ""
	}
	bindings := map[string]any{}
	for k, v := range au.bindings {
		bindings[k] = v
	}
	bindings["result"] = toJSValue(result)
	if au.source != nil {
		bindings["source"] = au.source
	}
	v, err := au.js.Eval(jsStr, bindings)
	if err != nil {
		return ""
	}
	return v
}

// BuildRequest 生成 httpx 请求。
func (au *AnalyzeUrl) BuildRequest(extraHeaders map[string]string) *httpx.Request {
	headers := map[string]string{}
	if au.source != nil {
		headers = httpx.ParseHeaders(au.source.GetHeader())
	}
	for k, v := range au.Headers {
		headers[k] = v
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}
	// Cookie 注入
	if au.cookies != nil {
		if c := au.cookies.GetCookie(au.URL); c != "" {
			headers["Cookie"] = c
		}
	}
	// 默认 Referer = 源站点首页（降低防盗链 403 概率）
	if headers["Referer"] == "" && au.source != nil && au.source.GetKey() != "" {
		if u, err := url.Parse(au.source.GetKey()); err == nil && u.Host != "" {
			scheme := u.Scheme
			if scheme != "http" && scheme != "https" {
				scheme = "https"
			}
			headers["Referer"] = scheme + "://" + u.Host + "/"
		}
	}
	method := au.Method
	if method == "" {
		method = "GET"
	}
	body := au.Body
	if method == "POST" && body != "" {
		ct := headers["Content-Type"]
		trimmed := strings.TrimSpace(body)
		isJSON := strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")
		isXML := strings.HasPrefix(trimmed, "<")
		if ct == "" {
			if isJSON {
				headers["Content-Type"] = "application/json; charset=UTF-8"
			} else if !isXML {
				headers["Content-Type"] = "application/x-www-form-urlencoded"
				body = encodeFormBody(body)
			}
		} else if strings.Contains(ct, "x-www-form-urlencoded") {
			body = encodeFormBody(body)
		}
	}
	req := &httpx.Request{
		Method:  method,
		URL:     au.URL,
		Headers: headers,
		Body:    body,
		Charset: au.Charset,
	}
	if au.TimeoutMS > 0 {
		req.Timeout = time.Duration(au.TimeoutMS) * time.Millisecond
	}
	return req
}

// encodeFormBody 将 {k:v,k2:v2} 或 k=v&k2=v2 形式规范化为表单体。
func encodeFormBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		var m map[string]any
		if err := jsonUnmarshalLoose([]byte(trimmed), &m); err == nil {
			vals := url.Values{}
			for k, v := range m {
				vals.Set(k, toStringAny(v))
			}
			return vals.Encode()
		}
	}
	return trimmed
}

func encodeQuery(s string) string {
	return url.QueryEscape(s)
}

// encodeURLQuery 对 query 中未编码的中文重新编码（已编码则原样）。
func encodeURLQuery(rawURL string) string {
	i := strings.Index(rawURL, "?")
	if i < 0 {
		return rawURL
	}
	base, query := rawURL[:i], rawURL[i+1:]
	// 已是合法 URL（无未编码中文/空格）则原样
	if strings.ContainsAny(query, " \u4e00-\u9fff") == false && !strings.ContainsFunc(query, func(r rune) bool { return r > 127 || r == ' ' }) {
		return rawURL
	}
	vals := url.Values{}
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		// 已编码的 v 不再编码
		if dec, err := url.QueryUnescape(v); err == nil && dec != v {
			vals.Set(k, v) // 保留原编码
		} else {
			vals.Set(k, v)
		}
	}
	return base + "?" + vals.Encode()
}

func toStringAny(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return string(b)
	}
}

func jsonUnmarshalLoose(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err == nil {
		return nil
	}
	// 单引号宽松
	s := strings.ReplaceAll(string(b), "'", `"`)
	return json.Unmarshal([]byte(s), v)
}

// bareKeyRe 匹配 { 或 , 之后的裸标识符键（GSON lenient 接受、encoding/json 拒绝）。
// 只处理字符串字面量之外的区域，避免误改 URL 值内部偶然出现的 ",word:" 形态。
var bareKeyRe = regexp.MustCompile(`([{,]\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:`)

// trailingCommaRe 匹配数组/对象末尾的逗号（legado 源常见笔误）。
var trailingCommaRe = regexp.MustCompile(`,\s*([}\]])`)

// gsonLenient 按 Java GSON lenient 模式的接受范围解析 JSON：
// 严格解析 → 单引号值替换 → 裸键补引号（字符串感知）→ 尾逗号清理 → 再解析。
// legado 书源 JSON 大量使用单引号值与无引号键（GSON 原生接受），encoding/json 严格拒绝。
func gsonLenient(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err == nil {
		return nil
	}
	s := strings.ReplaceAll(string(b), "'", `"`)
	s = quoteBareJSONKeys(s)
	s = trailingCommaRe.ReplaceAllString(s, `$1`)
	return json.Unmarshal([]byte(s), v)
}

// quoteBareJSONKeys 给 JSON 裸键补引号。字符串感知：逐字节扫描，
// 只对字符串字面量之外的区域应用裸键正则——字符串内部（如 URL 值里的
// ",word:"）原样保留。UTF-8 多字节序列不含 0x22/0x5C 字节，逐字节扫描安全。
func quoteBareJSONKeys(s string) string {
	var sb strings.Builder
	i, n := 0, len(s)
	segStart := 0 // 当前"字符串之外"区域的起点
	inStr := false
	for i < n {
		if inStr {
			if s[i] == '\\' && i+1 < n {
				i += 2 // 跳过转义对
				continue
			}
			if s[i] == '"' {
				sb.WriteString(s[segStart:i+1]) // 字符串字面量原样拷贝
				i++
				segStart = i
				inStr = false
				continue
			}
			i++
			continue
		}
		if s[i] == '"' {
			// 进入字符串前：把之前的非字符串区域做裸键补引号
			sb.WriteString(bareKeyRe.ReplaceAllString(s[segStart:i], `$1"$2":`))
			segStart = i
			inStr = true
			i++
			continue
		}
		i++
	}
	// 末尾非字符串区域
	sb.WriteString(bareKeyRe.ReplaceAllString(s[segStart:], `$1"$2":`))
	return sb.String()
}

// ExploreKind 发现页分类（exploreUrl 解析产物）。
type ExploreKind struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// AnalyzeExploreKinds 解析 legado exploreUrl 为分类列表。三种形态：
//  1. <js>...</js> / @js: → JS 求值出 JSON 数组（binding page/key/baseUrl 注入）
//  2. 直接 JSON 数组 [{"title":..,"url":..}] → 宽松解析
//  3. 纯 URL（无分类结构）→ 单分类"全部分类"兜底
// 分类 URL 里的 {{page}} 与 ,{options} 由 AnalyzeUrl 流水线在请求时处理，此处不展开。
// AnalyzeExploreKinds 解析 legado exploreUrl 为分类列表，语义对齐 legado
// BookSourceExtensions.exploreKinds()：
//  1. <js>...</js> / @js: → JS 求值出结果（binding page/key/baseUrl 注入）
//  2. 求值结果/原文为 JSON 数组 → 逐项取 title/url（url 为空的分组标题行保留判断由调用方处理——
//     legado 原版原样保留，这里为前端可用性过滤掉不可点项）
//  3. 否则按 legado "(&&|\n)+" 分隔符切分（除换行还支持 &&），每段 "标题::URL"；
//     url 为空的段（纯分组标题，如"♂::"）跳过
//  4. 纯 URL（无分类结构）→ 单分类"全部分类"兜底
// 分类 URL 里的 {{page}}、<page 数组> 与 ,{options} 由 AnalyzeUrl 流水线在请求时处理。
func AnalyzeExploreKinds(ruleURL, baseURL string, source BookSourceRef, js JSEngine, cookies CookieProvider) []ExploreKind {
	au, err := NewAnalyzeUrl("", "", 1, baseURL, source, js, nil, cookies, nil)
	if err != nil {
		return nil
	}
	// 先跑 JS 段（binding 已在构造时注入）
	expanded := au.analyzeJsMulti(ruleURL)
	expanded = strings.TrimSpace(expanded)

	// 形态 2：JSON 数组（GSON-lenient：单引号值/裸键/尾逗号宽容）
	if strings.HasPrefix(expanded, "[") {
		var raw []map[string]any
		if gsonLenient([]byte(expanded), &raw) == nil {
			out := make([]ExploreKind, 0, len(raw))
			for _, m := range raw {
				title, _ := m["title"].(string)
				url, _ := m["url"].(string)
				if title == "" || url == "" {
					continue
				}
				out = append(out, ExploreKind{Title: title, URL: url})
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	// 形态 3：legado 分隔语义 "(&&|\n)+"，每段 "标题::URL"。
	// url 为空的段是分组标题（如"♂::"），跳过。
	segRe := regexp.MustCompile(`&&|\n+`)
	out := make([]ExploreKind, 0, 16)
	for _, seg := range segRe.Split(expanded, -1) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		parts := strings.SplitN(seg, "::", 2)
		if len(parts) != 2 {
			continue
		}
		title := strings.TrimSpace(parts[0])
		url := strings.TrimSpace(parts[1])
		if title == "" || url == "" {
			continue // 分组标题段（url 为空）
		}
		out = append(out, ExploreKind{Title: title, URL: url})
	}
	if len(out) > 0 {
		return out
	}
	// 形态 4：纯 URL 兜底（含 ,{options} 尾缀——URL 整体保留）。
	// 收紧：内容以 [ 或 { 开头（明显是 JSON 结构）却解析失败时不再伪装成
	// 「全部分类」URL——那会产生一个点击必炸的假分类（GSON 宽松 JSON 击穿
	// 解析器的历史 bug）。
	if expanded != "" && !strings.HasPrefix(expanded, "[") && !strings.HasPrefix(expanded, "{") &&
		strings.Contains(expanded, "/") {
		return []ExploreKind{{Title: "全部分类", URL: expanded}}
	}
	return nil
}
