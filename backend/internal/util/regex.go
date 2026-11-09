package util

import (
	"regexp"

	regexp2 "github.com/dlclark/regexp2/v2"
)

// Regex 统一正则封装：优先 Go RE2（快），编译失败回退 regexp2（纯 Go，
// 支持 Java/.NET 风格的环视断言等 legado 规则常用语法）。
type Regex struct {
	re   *regexp.Regexp
	re2  *regexp2.Regexp
	expr string
}

// CompileRegex 编译正则（RE2 → regexp2 回退）。
func CompileRegex(expr string) (*Regex, error) {
	if re, err := regexp.Compile(expr); err == nil {
		return &Regex{re: re, expr: expr}, nil
	}
	re2, err := regexp2.Compile(expr, regexp2.None)
	if err != nil {
		return nil, err
	}
	return &Regex{re2: re2, expr: expr}, nil
}

// MustCompileRegex 同 CompileRegex，失败返回 nil。
func MustCompileRegex(expr string) *Regex {
	r, err := CompileRegex(expr)
	if err != nil {
		return nil
	}
	return r
}

// Expr 原始表达式。
func (r *Regex) Expr() string { return r.expr }

// MatchString 是否匹配。
func (r *Regex) MatchString(s string) bool {
	if r.re != nil {
		return r.re.MatchString(s)
	}
	ok, _ := r.re2.MatchString(s)
	return ok
}

// ReplaceAllString 全部替换（$1 组引用语义与 Java 一致）。
func (r *Regex) ReplaceAllString(s, repl string) string {
	if r.re != nil {
		return r.re.ReplaceAllString(s, repl)
	}
	out, err := r.re2.Replace(s, repl, -1, 0)
	if err != nil {
		return s
	}
	return out
}

// Match 查找首个匹配，返回完整匹配文本；未命中返回 ("", false)。
func (r *Regex) Match(s string) (string, bool) {
	if r.re != nil {
		m := r.re.FindString(s)
		return m, m != "" || r.re.MatchString(s)
	}
	m, err := r.re2.FindStringMatch(s)
	if err != nil || m == nil {
		return "", false
	}
	return m.String(), true
}

// FindAll 全部匹配（按序返回完整匹配文本）。
func (r *Regex) FindAll(s string) []string {
	if r.re != nil {
		return r.re.FindAllString(s, -1)
	}
	var out []string
	m, _ := r.re2.FindStringMatch(s)
	for m != nil {
		out = append(out, m.String())
		m, _ = r.re2.FindNextMatch(m)
	}
	return out
}

// GroupMatches 逐匹配迭代（供 JS 逐匹配替换等场景）。
// fn 接收完整匹配文本，返回替换文本。
func (r *Regex) GroupMatches(s string, fn func(match string) string) string {
	if r.re != nil {
		locs := r.re.FindAllStringIndex(s, -1)
		if len(locs) == 0 {
			return s
		}
		var sb []byte
		last := 0
		for _, loc := range locs {
			sb = append(sb, s[last:loc[0]]...)
			sb = append(sb, fn(s[loc[0]:loc[1]])...)
			last = loc[1]
		}
		sb = append(sb, s[last:]...)
		return string(sb)
	}
	var sb []byte
	last := 0
	m, _ := r.re2.FindStringMatch(s)
	for m != nil {
		// RuneIndex/RuneLength 以 rune 计，需换算为字节索引
		bStart := runeIndexToByte(s, m.RuneIndex)
		bEnd := runeIndexToByte(s, m.RuneIndex+m.RuneLength)
		sb = append(sb, s[last:bStart]...)
		sb = append(sb, fn(s[bStart:bEnd])...)
		last = bEnd
		m, _ = r.re2.FindNextMatch(m)
	}
	sb = append(sb, s[last:]...)
	return string(sb)
}

// runeIndexToByte 把 rune 索引转为字节索引。
func runeIndexToByte(s string, runeIdx int) int {
	if runeIdx <= 0 {
		return 0
	}
	count := 0
	for i := range s {
		if count == runeIdx {
			return i
		}
		count++
	}
	return len(s)
}
