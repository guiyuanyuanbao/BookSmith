// Package util 提供全书通用的文本处理工具。
package util

import (
	"html"
	"path"
	"regexp"
	"strings"
	"unicode"
)

// 不可见字符（借鉴 so-novel 的乱码根因分析）：控制字符、格式控制符、私有区 PUA、行/段分隔符、零宽字符。
var invisibleChars = regexp.MustCompile(`[\p{C}\p{Cf}\p{Co}\p{Zl}\p{Zp}\x{200B}\x{FEFF}]`)

// CleanInvisibleChars 移除不可见字符（保留 \n \r \t）。
func CleanInvisibleChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			b.WriteRune(r)
		default:
			if invisibleChars.MatchString(string(r)) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

var winReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true,
	"COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// invalidNameChars 匹配 Windows 文件名非法字符及控制字符。
var invalidNameChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

// SanitizeFileName 清理文件名：移除 Windows 非法字符、保留字、首尾空白与点。
func SanitizeFileName(name string) string {
	name = invalidNameChars.ReplaceAllString(name, "")
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".")
	if name == "" || winReserved[strings.ToUpper(name)] {
		return "_"
	}
	// 限制长度，避免路径超长
	const maxLen = 100
	r := []rune(name)
	if len(r) > maxLen {
		name = string(r[:maxLen])
	}
	return name
}

// SafeBookFileName 生成 "书名(作者)" 形式的安全文件主名。
func SafeBookFileName(name, author string) string {
	base := SanitizeFileName(name)
	if author != "" {
		return base + "(" + SanitizeFileName(author) + ")"
	}
	return base
}

// DecodeHTMLEntities 解码 HTML 实体（&nbsp; 等命名实体与 &#x; 数字实体）。
func DecodeHTMLEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return html.UnescapeString(s)
}

// RemoveBlankLines 去除空白行，保留单个换行。
func RemoveBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, strings.TrimRight(line, "\r"))
	}
	return strings.Join(out, "\n")
}

// HalfWidth 将全角数字/字母转半角（legado 部分源需要）。
func HalfWidth(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '０' && r <= '９':
			b.WriteRune(r - 0xFEE0)
		case r >= 'Ａ' && r <= 'Ｚ':
			b.WriteRune(r - 0xFEE0)
		case r >= 'ａ' && r <= 'ｚ':
			b.WriteRune(r - 0xFEE0)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeBookKey 书籍去重键：书名+作者，去除空格/全半角差异后小写。
func NormalizeBookKey(name, author string) string {
	norm := func(s string) string {
		s = HalfWidth(s)
		s = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, s)
		return strings.ToLower(s)
	}
	return norm(name) + "|" + norm(author)
}

// ExtOf 返回 URL/路径的小写扩展名（不含点），无扩展名返回空。
func ExtOf(u string) string {
	if u == "" {
		return ""
	}
	clean := strings.SplitN(u, "?", 2)[0]
	clean = strings.SplitN(clean, "#", 2)[0]
	return strings.TrimPrefix(path.Ext(strings.ToLower(clean)), ".")
}

// ReverseAny 任意切片反转（泛型）。
func ReverseAny[T any](list []T) {
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
}

// SameURL 比较两个 URL 是否等价（忽略末尾斜杠与 fragment）。
func SameURL(a, b string) bool {
	na := strings.TrimRight(strings.SplitN(a, "#", 2)[0], "/")
	nb := strings.TrimRight(strings.SplitN(b, "#", 2)[0], "/")
	return na == nb && na != ""
}
