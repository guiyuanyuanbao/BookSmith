package analyzer

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"booksmith/internal/util"
	"booksmith/internal/varstore"
)

// Mode 规则模式（对应 legado Mode）。
type Mode int

const (
	ModeXPath Mode = iota
	ModeJson
	ModeDefault
	ModeJs
	ModeRegex
	ModeWebJs
)

// JSEngine JS 执行引擎接口（由 jsenv 包以 goja 实现，保持 analyzer 无桥接依赖）。
type JSEngine interface {
	// Eval 执行脚本并返回 Go 值；出错返回 error。
	Eval(script string, bindings map[string]any) (any, error)
}

// VariableProvider 变量存取（chapter/book/ruleData/source 层级由实现组织）。
type VariableProvider = varstore.VariableProvider

// Variables 变量存储（varstore 实现）。
type Variables = varstore.Variables

// NewVariables 创建变量存储。
func NewVariables(parent VariableProvider) *Variables { return varstore.New(parent) }

// AnalyzeRule 规则调度中心（legado AnalyzeRule 的移植）。
type AnalyzeRule struct {
	content any
	isJSON  bool
	baseURL string
	// redirectURL 用于相对 URL 绝对化的基准（通常是最终请求 URL）
	redirectURL string

	source   BookSourceRef
	js       JSEngine
	vars     VariableProvider
	bindings map[string]any // 额外 JS 绑定（page/key/book 等）

	// get("bookName")/get("title") 特判
	bookName     string
	chapterTitle string

	// 缓存子解析器（作用于 content 本身时复用）
	byJSoup *AnalyzeByJSoup
	byXPath *AnalyzeByXPath
	byJSON  *AnalyzeByJSONPath
}

// NewAnalyzeRule 创建规则分析器。
func NewAnalyzeRule(source BookSourceRef, js JSEngine, vars VariableProvider) *AnalyzeRule {
	return &AnalyzeRule{source: source, js: js, vars: vars, bindings: map[string]any{}}
}

// SetContent 设置解析内容（string/HTMLDoc/*html.Node/JSON 值）。
func (ar *AnalyzeRule) SetContent(content any, baseURL string) *AnalyzeRule {
	if content == nil {
		panic("内容不可空（Content cannot be null）")
	}
	ar.content = content
	switch c := content.(type) {
	case HTMLDoc:
		ar.isJSON = false
	case *HTMLDoc:
		ar.isJSON = false
	case string:
		ar.isJSON = IsJSONContent(c)
	default:
		// 已解析 JSON 值（map/[]any）视为 JSON
		switch content.(type) {
		case map[string]any, []any:
			ar.isJSON = true
		default:
			if s := fmt.Sprintf("%v", content); IsJSONContent(s) {
				ar.isJSON = true
			} else {
				ar.isJSON = false
			}
		}
	}
	if baseURL != "" {
		ar.baseURL = baseURL
	}
	ar.byJSoup = nil
	ar.byXPath = nil
	ar.byJSON = nil
	return ar
}

// SetRedirectURL 设置重定向后的 URL（相对链接绝对化基准）。
func (ar *AnalyzeRule) SetRedirectURL(url string) {
	ar.redirectURL = url
}

// SetBaseURL 设置基准 URL。
func (ar *AnalyzeRule) SetBaseURL(url string) {
	if url != "" {
		ar.baseURL = url
	}
}

// Bind 注入额外 JS 绑定（page/key/book/source 等）。
func (ar *AnalyzeRule) Bind(key string, value any) *AnalyzeRule {
	ar.bindings[key] = value
	return ar
}

// SetBookName 注入书名（get("bookName")）。
func (ar *AnalyzeRule) SetBookName(name string) { ar.bookName = name }

// SetChapterTitle 注入章节标题（get("title")）。
func (ar *AnalyzeRule) SetChapterTitle(t string) { ar.chapterTitle = t }

// Put 保存变量。
func (ar *AnalyzeRule) Put(key, value string) string {
	if ar.vars != nil {
		ar.vars.Put(key, value)
	}
	return value
}

// Get 读取变量。
func (ar *AnalyzeRule) Get(key string) string {
	switch key {
	case "bookName":
		if ar.bookName != "" {
			return ar.bookName
		}
	case "title":
		if ar.chapterTitle != "" {
			return ar.chapterTitle
		}
	}
	if ar.vars != nil {
		return ar.vars.Get(key)
	}
	return ""
}

// ---------------- 子解析器 ----------------

func (ar *AnalyzeRule) getAnalyzeByJSoup(o any) (*AnalyzeByJSoup, error) {
	if sameContent(o, ar.content) && ar.byJSoup != nil {
		return ar.byJSoup, nil
	}
	a, err := NewAnalyzeByJSoup(o)
	if err != nil {
		return nil, err
	}
	if sameContent(o, ar.content) {
		ar.byJSoup = a
	}
	return a, nil
}

func (ar *AnalyzeRule) getAnalyzeByXPath(o any) (*AnalyzeByXPath, error) {
	if sameContent(o, ar.content) && ar.byXPath != nil {
		return ar.byXPath, nil
	}
	a, err := NewAnalyzeByXPath(o)
	if err != nil {
		return nil, err
	}
	if sameContent(o, ar.content) {
		ar.byXPath = a
	}
	return a, nil
}

func (ar *AnalyzeRule) getAnalyzeByJSON(o any) (*AnalyzeByJSONPath, error) {
	if sameContent(o, ar.content) && ar.byJSON != nil {
		return ar.byJSON, nil
	}
	a, err := NewAnalyzeByJSONPath(o)
	if err != nil {
		return nil, err
	}
	if sameContent(o, ar.content) {
		ar.byJSON = a
	}
	return a, nil
}

func sameContent(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if an, ok := a.(HTMLDoc); ok {
		a = an.Node
	}
	if bn, ok := b.(HTMLDoc); ok {
		b = bn.Node
	}
	switch av := a.(type) {
	case *html.Node:
		bn, ok := b.(*html.Node)
		return ok && av == bn
	case string:
		bs, ok := b.(string)
		return ok && av == bs
	default:
		// map/slice 等不可比较类型：不缓存子解析器
		return false
	}
}

// ---------------- 正则缓存 ----------------

var regexCache = newLRUCache(16)

func compileRegexCached(expr string) *util.Regex {
	if v, ok := regexCache.get(expr); ok {
		if re, ok := v.(*util.Regex); ok {
			return re
		}
		return nil
	}
	re, err := util.CompileRegex(expr)
	if err != nil {
		regexCache.put(expr, nil)
		return nil
	}
	regexCache.put(expr, re)
	return re
}

// replaceRegex 实现 legado ## 正则替换语义。
func replaceRegex(result string, replaceRegex, replacement string, replaceFirst bool) string {
	if replaceRegex == "" {
		return result
	}
	re := compileRegexCached(replaceRegex)
	if replaceFirst {
		// ##match##replace###：取第一个匹配到的结果并替换，找不到整体清空
		if re != nil {
			m, ok := re.Match(result)
			if !ok {
				return ""
			}
			return strings.Replace(m, replaceRegex, replacement, 1)
			// 与 Kotlin 语义一致 —— group(0).replaceFirst(regex, replacement)
		}
		return replacement
	}
	if re != nil {
		return re.ReplaceAllString(result, replacement)
	}
	// 正则编译失败：退化为字面替换
	return strings.ReplaceAll(result, replaceRegex, replacement)
}

// ---------------- SourceRule ----------------

var (
	jsPattern    = regexp.MustCompile(`(?is)<js>([\w\W]*?)</js>|@js:([\w\W]*)`)
	webJSPattern = regexp.MustCompile(`(?is)@webjs:([\w\W]{5,})`)
	putPattern   = regexp.MustCompile(`(?i)@put:(\{[^}]+?\})`)
	evalPattern  = regexp.MustCompile(`(?i)@get:\{[^}]+?\}|\{\{[\w\W]*?\}\}`)
	regexPattern = regexp.MustCompile(`\$\d{1,2}`)
)

// SourceRule 单条规则。
type SourceRule struct {
	mode         Mode
	rule         string
	replaceRegex string
	replacement  string
	replaceFirst bool
	putMap       map[string]string
	ruleParam    []string
	ruleType     []int
}

const (
	getRuleType     = -2
	jsRuleType      = -1
	defaultRuleType = 0
)

// SplitSourceRule 分解规则生成规则列表（legado splitSourceRule 移植）。
func SplitSourceRule(ruleStr string, allInOne bool, isRegexFlag bool) []*SourceRule {
	if ruleStr == "" {
		return nil
	}
	var ruleList []*SourceRule
	mode := ModeDefault
	start := 0
	if allInOne && strings.HasPrefix(ruleStr, ":") {
		mode = ModeRegex
		isRegexFlag = true
		start = 1
	} else if isRegexFlag {
		mode = ModeRegex
	}

	locs := jsPattern.FindAllStringSubmatchIndex(ruleStr, -1)
	for _, loc := range locs {
		if loc[0] > start {
			tmp := strings.TrimSpace(ruleStr[start:loc[0]])
			if tmp != "" {
				ruleList = append(ruleList, newSourceRule(tmp, mode))
			}
		}
		// group(2)（@js: 形式）优先，否则 group(1)（<js></js> 形式）
		var jsBody string
		if loc[4] >= 0 {
			jsBody = ruleStr[loc[4]:loc[5]]
		} else if loc[2] >= 0 {
			jsBody = ruleStr[loc[2]:loc[3]]
		}
		ruleList = append(ruleList, newSourceRule(jsBody, ModeJs))
		start = loc[1]
	}
	locsW := webJSPattern.FindAllStringSubmatchIndex(ruleStr, -1)
	for _, loc := range locsW {
		if loc[0] > start {
			tmp := strings.TrimSpace(ruleStr[start:loc[0]])
			if tmp != "" {
				ruleList = append(ruleList, newSourceRule(tmp, mode))
			}
		}
		var body string
		if loc[2] >= 0 {
			body = ruleStr[loc[2]:loc[3]]
		}
		ruleList = append(ruleList, newSourceRule(body, ModeWebJs))
		start = loc[1]
	}
	if len(ruleStr) > start {
		tmp := strings.TrimSpace(ruleStr[start:])
		if tmp != "" {
			ruleList = append(ruleList, newSourceRule(tmp, mode))
		}
	}
	return ruleList
}

func newSourceRule(ruleStr string, mode Mode) *SourceRule {
	sr := &SourceRule{mode: mode, putMap: map[string]string{}}
	sr.init(ruleStr)
	return sr
}

func (sr *SourceRule) init(ruleStr string) {
	rule := ruleStr
	switch {
	case sr.mode == ModeJs || sr.mode == ModeRegex:
		// 保持
	case strings.HasPrefix(strings.ToLower(ruleStr), "@css:"):
		sr.mode = ModeDefault
		// 保留全串，由 AnalyzeByJSoup 处理
	case strings.HasPrefix(ruleStr, "@@"):
		sr.mode = ModeDefault
		rule = ruleStr[2:]
	case strings.HasPrefix(strings.ToLower(ruleStr), "@xpath:"):
		sr.mode = ModeXPath
		rule = ruleStr[7:]
	case strings.HasPrefix(strings.ToLower(ruleStr), "@json:"):
		sr.mode = ModeJson
		rule = ruleStr[6:]
	case strings.HasPrefix(ruleStr, "$.") || strings.HasPrefix(ruleStr, "$["):
		sr.mode = ModeJson
	case strings.HasPrefix(ruleStr, "/"):
		sr.mode = ModeXPath
	}
	// isJSON 强制 Json 模式由 SplitSourceRule 的调用环境处理不了（init 无 content 上下文），
	// 因此 AnalyzeRule 侧在 getString 系列中对 isJSON 内容强制覆盖 mode。

	// 分离 @put
	rule = splitPutRule(rule, sr.putMap)

	// @get 与 {{}} 拆分
	start := 0
	locs := evalPattern.FindAllStringIndex(rule, -1)
	if len(locs) > 0 {
		pre := rule[0:locs[0][0]]
		if sr.mode != ModeJs && sr.mode != ModeRegex && (locs[0][0] == 0 || !strings.Contains(pre, "##")) {
			sr.mode = ModeRegex
		}
		for _, loc := range locs {
			if loc[0] > start {
				sr.splitRegex(rule[start:loc[0]])
			}
			tmp := rule[loc[0]:loc[1]]
			lt := strings.ToLower(tmp)
			switch {
			case strings.HasPrefix(lt, "@get:"):
				sr.ruleType = append(sr.ruleType, getRuleType)
				sr.ruleParam = append(sr.ruleParam, tmp[6:len(tmp)-1])
			case strings.HasPrefix(tmp, "{{"):
				sr.ruleType = append(sr.ruleType, jsRuleType)
				sr.ruleParam = append(sr.ruleParam, tmp[2:len(tmp)-2])
			default:
				sr.splitRegex(tmp)
			}
			start = loc[1]
		}
	}
	if len(rule) > start {
		sr.splitRegex(rule[start:])
	}
	sr.rule = rule
}

// splitPutRule 分离 @put:{...} 规则。
func splitPutRule(ruleStr string, putMap map[string]string) string {
	v := ruleStr
	locs := putPattern.FindAllStringSubmatchIndex(v, -1)
	if len(locs) == 0 {
		return v
	}
	var replaces []string
	for _, loc := range locs {
		full := v[loc[0]:loc[1]]
		jsonStr := v[loc[2]:loc[3]]
		replaces = append(replaces, full)
		var m map[string]string
		if err := jsonStrictUnmarshal([]byte(jsonStr), &m); err == nil {
			for k, val := range m {
				putMap[k] = val
			}
			continue
		}
		if err := jsonLooseUnmarshal([]byte(jsonStr), &m); err == nil {
			for k, val := range m {
				putMap[k] = val
			}
		}
	}
	for _, r := range replaces {
		v = strings.ReplaceAll(v, r, "")
	}
	return v
}

// splitRegex 拆分 $N 引用。
func (sr *SourceRule) splitRegex(ruleStr string) {
	start := 0
	parts := strings.Split(ruleStr, "##")
	locs := regexPattern.FindAllStringIndex(parts[0], -1)
	if len(locs) > 0 {
		if sr.mode != ModeJs && sr.mode != ModeRegex {
			sr.mode = ModeRegex
		}
		for _, loc := range locs {
			if loc[0] > start {
				sr.ruleType = append(sr.ruleType, defaultRuleType)
				sr.ruleParam = append(sr.ruleParam, ruleStr[start:loc[0]])
			}
			tmp := ruleStr[loc[0]:loc[1]]
			n := 0
			fmt.Sscanf(tmp[1:], "%d", &n)
			sr.ruleType = append(sr.ruleType, n)
			sr.ruleParam = append(sr.ruleParam, tmp)
			start = loc[1]
		}
	}
	if len(ruleStr) > start {
		sr.ruleType = append(sr.ruleType, defaultRuleType)
		sr.ruleParam = append(sr.ruleParam, ruleStr[start:])
	}
}

// makeUpRule 重建规则文本：替换 $N / @get / {{}}；末尾切分 ## 正则参数。
func (sr *SourceRule) makeUpRule(result any, evalJSEval func(js string, result any) any, getFn func(key string) string, getStringFn func(rule string) string) {
	if len(sr.ruleParam) > 0 {
		var infoVal strings.Builder
		segments := make([]string, 0, len(sr.ruleParam))
		for index := len(sr.ruleParam) - 1; index >= 0; index-- {
			regType := sr.ruleType[index]
			var seg string
			switch {
			case regType > defaultRuleType:
				inserted := false
				if list, ok := result.([]string); ok && len(list) > regType {
					seg = list[regType]
					inserted = true
				} else if list, ok := result.([]any); ok && len(list) > regType {
					seg = fmt.Sprintf("%v", list[regType])
					inserted = true
				}
				if !inserted {
					seg = sr.ruleParam[index]
				}
			case regType == jsRuleType:
				if isRuleText(sr.ruleParam[index]) {
					seg = getStringFn(sr.ruleParam[index])
				} else {
					v := evalJSEval(sr.ruleParam[index], result)
					seg = jsResultToString(v)
				}
			case regType == getRuleType:
				seg = getFn(sr.ruleParam[index])
			default:
				seg = sr.ruleParam[index]
			}
			segments = append(segments, seg)
		}
		// 逆序收集，正序拼接
		infoVal.Reset()
		for i := len(segments) - 1; i >= 0; i-- {
			infoVal.WriteString(segments[i])
		}
		sr.rule = infoVal.String()
	}
	// 分离正则表达式
	parts := strings.Split(sr.rule, "##")
	sr.rule = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		sr.replaceRegex = parts[1]
	}
	if len(parts) > 2 {
		sr.replacement = parts[2]
	}
	if len(parts) > 3 {
		sr.replaceFirst = true
	}
}

func isRuleText(s string) bool {
	return strings.HasPrefix(s, "@") || strings.HasPrefix(s, "$.") ||
		strings.HasPrefix(s, "$[") || strings.HasPrefix(s, "//")
}

// jsResultToString JS 结果转字符串（Double 整除取整，对齐 legado）。
func jsResultToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%.0f", x)
		}
		return fmt.Sprintf("%v", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// GetParamSize 段数量（含 {{}} 组合判断）。
func (sr *SourceRule) GetParamSize() int { return len(sr.ruleParam) }

// ForceModeJson 强制 JSON 模式（内容为 JSON 时）。
func (sr *SourceRule) ForceModeJson() { sr.mode = ModeJson }
