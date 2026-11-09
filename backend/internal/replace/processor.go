// Package replace 实现 legado ReplaceRule 净化替换管线：
// scope 子串匹配、正则/纯文本/js 替换三模式、标题与正文分离、超时保护。
package replace

import (
	"fmt"
	"strings"
	"time"

	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/util"
	"booksmith/internal/varstore"
)

// Processor 净化处理器。
type Processor struct {
	rules []*model.ReplaceRule
	js    *jsenv.Engine
	vars  varstore.VariableProvider
	logFn func(string)
}

// NewProcessor 创建处理器。js 为空时忽略 @js: 替换。
func NewProcessor(rules []*model.ReplaceRule, js *jsenv.Engine, vars varstore.VariableProvider) *Processor {
	if vars == nil {
		vars = varstore.Empty{}
	}
	return &Processor{rules: rules, js: js, vars: vars, logFn: func(string) {}}
}

// SetLogFn 设置日志。
func (p *Processor) SetLogFn(fn func(string)) { p.logFn = fn }

// Rules 当前规则。
func (p *Processor) Rules() []*model.ReplaceRule { return p.rules }

// applyScope 作用域匹配（legado SQL LIKE 语义：scope 为书名/书源 URL 子串，空为全局）。
func applyScope(r *model.ReplaceRule, bookName, origin string) bool {
	// excludeScope 排除
	if ex := strings.TrimSpace(r.ExcludeScope); ex != "" {
		for _, item := range strings.Split(ex, ";") {
			item = strings.TrimSpace(item)
			if item != "" && (strings.Contains(bookName, item) || strings.Contains(origin, item)) {
				return false
			}
		}
	}
	scope := strings.TrimSpace(r.Scope)
	if scope == "" {
		return true
	}
	for _, item := range scopeSplit(scope) {
		item = strings.TrimSpace(item)
		if item != "" && (strings.Contains(bookName, item) || strings.Contains(origin, item)) {
			return true
		}
	}
	return false
}

// scopeSplit 作用域分隔（legado 用逗号/分号分隔多值）。
func scopeSplit(s string) []string {
	seps := strings.NewReplacer(";", ",", "；", ",", "，", ",").Replace(s)
	return strings.Split(seps, ",")
}

// ruleFor 判断规则是否适用（title/content 维度）。
func ruleFor(r *model.ReplaceRule, isTitle bool, bookName, origin string) bool {
	if !applyScope(r, bookName, origin) {
		return false
	}
	if isTitle {
		return r.ScopeTitle || r.ApplyTo == string(model.ApplyTitle) || r.ApplyTo == string(model.ApplyAll)
	}
	return r.ScopeContent || r.ApplyTo == string(model.ApplyContent) || r.ApplyTo == string(model.ApplyAll) || r.ApplyTo == ""
}

// ApplyTitle 应用标题替换。
func (p *Processor) ApplyTitle(title, bookName, origin string) string {
	for _, r := range p.rules {
		if r.Pattern == "" || !ruleFor(r, true, bookName, origin) {
			continue
		}
		title = p.applyRule(r, title)
	}
	return strings.TrimSpace(title)
}

// ApplyContent 应用正文替换（先按行 trim，与 legado 一致）。
func (p *Processor) ApplyContent(content, bookName, origin string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t　")
	}
	content = strings.Join(lines, "\n")
	for _, r := range p.rules {
		if r.Pattern == "" || !ruleFor(r, false, bookName, origin) {
			continue
		}
		content = p.applyRule(r, content)
	}
	return content
}

// applyRule 应用单条规则。
func (p *Processor) applyRule(r *model.ReplaceRule, content string) string {
	replacement := r.Replacement
	// @js: 替换模式
	if strings.HasPrefix(strings.ToLower(replacement), "@js:") {
		jsBody := replacement[4:]
		if r.IsRegex {
			return p.jsReplace(r, content, jsBody)
		}
		// 纯文本 + js：整体作为 result 执行
		return p.evalReplaceJS(r, jsBody, content, content)
	}
	if r.IsRegex {
		re, err := util.CompileRegex(r.Pattern)
		if err != nil {
			p.logFn(fmt.Sprintf("净化规则 %q 正则编译失败: %v", r.Name, err))
			return content
		}
		return re.ReplaceAllString(content, replacement)
	}
	return strings.ReplaceAll(content, r.Pattern, replacement)
}

// jsReplace 对每个正则匹配执行 JS（legado RegexExtensions 语义）。
func (p *Processor) jsReplace(r *model.ReplaceRule, content, jsBody string) string {
	re, err := util.CompileRegex(r.Pattern)
	if err != nil {
		p.logFn(fmt.Sprintf("净化规则 %q 正则编译失败: %v", r.Name, err))
		return content
	}
	jsBody = strings.TrimSpace(jsBody)
	return re.GroupMatches(content, func(group string) string {
		out := p.evalReplaceJS(r, jsBody, group, content)
		if out == groupTimedOut {
			return group // 超时保留原文
		}
		return out
	})
}

// groupTimedOut 标记超时。
const groupTimedOut = "\x00__timeout__\x00"

// evalReplaceJS 执行替换 JS：绑定 result/chapter/book/java。
func (p *Processor) evalReplaceJS(r *model.ReplaceRule, jsBody, result, full string) string {
	if p.js == nil {
		return result
	}
	bindings := map[string]any{
		"result":  result,
		"content": full,
		"chapter": map[string]any{},
		"book":    map[string]any{},
		"java":    newRegexJava(p.vars),
	}
	out, err := p.js.EvalWithTimeout(jsBody, bindings, time.Duration(r.EffectiveTimeout())*time.Millisecond)
	if err != nil {
		p.logFn(fmt.Sprintf("净化规则 %q JS 出错: %v", r.Name, err))
		return result
	}
	if s, ok := out.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", out)
}

// regexJava 净化规则内的 java 对象（put/get/log/t2s/s2t）。
type regexJava struct {
	vars varstore.VariableProvider
}

func newRegexJava(vars varstore.VariableProvider) *regexJava {
	return &regexJava{vars: vars}
}

func (j *regexJava) Put(key, value string) string {
	j.vars.Put(key, value)
	return value
}

func (j *regexJava) Get(args ...any) string {
	if len(args) == 0 {
		return ""
	}
	key := fmt.Sprintf("%v", args[0])
	return j.vars.Get(key)
}

func (j *regexJava) Log(msg string) string { return msg }

// sameTitleLineRe 正文首行 "书名…标题" 模式。
var sameTitleLineRe = util.MustCompileRegex(`^[\s　]*(书名[：:]|《.*?》)[\s　]*$|^[\s　]*.*?第\s*\d+\s*[章节][\s　]*$`)

// RemoveDuplicateTitle 去重复标题：正文首行为 书名/标题 变体时删除。
func RemoveDuplicateTitle(content, bookName, title string) string {
	lines := strings.SplitN(content, "\n", 2)
	first := strings.TrimSpace(lines[0])
	if first == "" {
		return content
	}
	if bookName != "" && strings.Contains(first, bookName) {
		return dropFirstLine(content)
	}
	if title != "" && strings.Contains(first, title) {
		return dropFirstLine(content)
	}
	return content
}

func dropFirstLine(content string) string {
	_, rest, ok := strings.Cut(content, "\n")
	if !ok {
		return ""
	}
	return strings.TrimLeft(rest, " \t")
}
