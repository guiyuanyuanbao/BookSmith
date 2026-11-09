package analyzer

import (
	"encoding/json"
	"fmt"
	stdhtml "html"
	"net/url"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
)

// toStringValue 任意值转字符串（JSON 值友好化）。
func toStringValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case *xhtml.Node:
		if x == nil {
			return ""
		}
		return ElementText(x)
	case HTMLDoc:
		return ElementText(x.Node)
	case *HTMLDoc:
		if x == nil {
			return ""
		}
		return ElementText(x.Node)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%.0f", x)
		}
		return fmt.Sprintf("%v", x)
	case bool:
		return fmt.Sprintf("%v", x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			parts = append(parts, toStringValue(item))
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return string(b)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// asJSObject 判断值是否为"JS 对象"（map），供键值直取优化。
func asJSObject(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	default:
		return nil, false
	}
}

// objectKeyString 从 map 中按键取值并转字符串。
func objectKeyString(mp map[string]any, key string) any {
	if v, ok := mp[key]; ok {
		return v
	}
	return ""
}

// normalizeJSONRoot 让 map 根可被 ojg 查询。
func normalizeJSONRoot(mp map[string]any) any { return mp }

// toJSValue 转换值为 JS 可消费的形态。
func toJSValue(v any) any {
	switch x := v.(type) {
	case HTMLDoc:
		return OuterHTML(x.Node)
	case *HTMLDoc:
		if x == nil {
			return ""
		}
		return OuterHTML(x.Node)
	case *xhtml.Node:
		if x == nil {
			return ""
		}
		return OuterHTML(x)
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, toJSValue(item))
		}
		return out
	default:
		return x
	}
}

// nodesToAny 元素切片转 []any。
func nodesToAny(nodes []*xhtml.Node) []any {
	out := make([]any, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n)
	}
	return out
}

// UnescapeHTML 解码 HTML 实体（unescapeHtml4 语义）。
func UnescapeHTML(s string) string {
	return stdhtml.UnescapeString(s)
}

// AbsoluteURLString 相对 URL 绝对化（NetworkUtils.getAbsoluteURL 语义）。
// data: URL 原样返回；javascript: 返回空串；空串返回空串。
func AbsoluteURLString(baseURL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	low := strings.ToLower(ref)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") ||
		strings.HasPrefix(low, "data:") || strings.HasPrefix(low, "file:") {
		return ref
	}
	if strings.HasPrefix(low, "javascript:") {
		return ""
	}
	if baseURL == "" {
		return ref
	}
	bu, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ref
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if ru.Host != "" && ru.Scheme == "" && bu.Scheme != "" {
		return bu.Scheme + ":" + ref
	}
	abs := bu.ResolveReference(ru)
	return abs.String()
}

// splitNotBlank 按分隔符切分并过滤空段。
func splitNotBlank(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------- AnalyzeByRegex ----------------

// RegexGetElement 正则链取单个匹配的分组列表（legado AnalyzeByRegex.getElement）。
func RegexGetElement(res string, regs []string) []string {
	return regexGetElement(res, regs, 0)
}

func regexGetElement(res string, regs []string, idx int) []string {
	if idx >= len(regs) {
		return nil
	}
	re, err := regexp.Compile(regs[idx])
	if err != nil {
		return nil
	}
	locs := re.FindAllStringSubmatchIndex(res, -1)
	if len(locs) == 0 {
		return nil
	}
	if idx+1 == len(regs) {
		var info []string
		for gi := 0; gi <= re.NumSubexp(); gi++ {
			loc := locs[0]
			if 2*gi+1 >= len(loc) || loc[2*gi] < 0 {
				info = append(info, "")
				continue
			}
			info = append(info, res[loc[2*gi]:loc[2*gi+1]])
		}
		return info
	}
	var sb strings.Builder
	for _, loc := range locs {
		sb.WriteString(res[loc[0]:loc[1]])
	}
	return regexGetElement(sb.String(), regs, idx+1)
}

// RegexGetElements 正则链取分组列表的列表（legado AnalyzeByRegex.getElements）。
func RegexGetElements(res string, regs []string) [][]string {
	return regexGetElements(res, regs, 0)
}

func regexGetElements(res string, regs []string, idx int) [][]string {
	if idx >= len(regs) {
		return nil
	}
	re, err := regexp.Compile(regs[idx])
	if err != nil {
		return nil
	}
	locs := re.FindAllStringSubmatchIndex(res, -1)
	if len(locs) == 0 {
		return nil
	}
	if idx+1 == len(regs) {
		var books [][]string
		for _, loc := range locs {
			var info []string
			// loc[2g]:loc[2g+1] 为第 g 个子匹配（0 为整体）
			for gi := 0; gi <= re.NumSubexp(); gi++ {
				if 2*gi+1 >= len(loc) {
					break
				}
				if loc[2*gi] < 0 {
					info = append(info, "")
					continue
				}
				info = append(info, res[loc[2*gi]:loc[2*gi+1]])
			}
			books = append(books, info)
		}
		return books
	}
	var sb strings.Builder
	for _, loc := range locs {
		sb.WriteString(res[loc[0]:loc[1]])
	}
	return regexGetElements(sb.String(), regs, idx+1)
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
