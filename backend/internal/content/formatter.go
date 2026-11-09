// Package content 提供正文 HTML 格式化（legado HtmlFormatter 语义对齐）。
package content

import (
	"regexp"
	"strings"

	"booksmith/internal/analyzer"
)

var (
	nbspRe    = regexp.MustCompile(`(&nbsp;)+`)
	espRe     = regexp.MustCompile(`&ensp;|&emsp;`)
	noPrintRe = regexp.MustCompile(`&thinsp;|&zwnj;|&zwj|\x{2009}|\x{200C}|\x{200D}`)
	wrapRe    = regexp.MustCompile(`(?i)</?(?:div|p|br|hr|h\d|article|dd|dl)[^>]*>`)
	commentRe = regexp.MustCompile(`<!--[^>]*-->`)
	// 标签删除（keepImg 时保留 <img>）
	tagRe = regexp.MustCompile(`(?i)</?[a-zA-Z][a-zA-Z0-9]*[^<>]*>`)

	indent1Re = regexp.MustCompile(`\s*\n+\s*`)
	indent2Re = regexp.MustCompile(`^[\n\s]+`)
	lastRe    = regexp.MustCompile(`[\n\s]+$`)
)

// formatImagePattern 图片 src 提取（legado 语义，优先级：
// src 含 ,{...} 参数 > data-src/data-original/data-srcset > src > 其他 data-*）。
var formatImageRe = regexp.MustCompile(`(?i)<img[^>]*\ssrc\s*=\s*['"]([^'"{>]*\{(?:[^{}]|\{[^}>]+\})+\})['"][^>]*>|<img[^>]*\sdata-(?:src|original|srcset)\s*=\s*['"]([^'">]+)['"][^>]*>|<img[^>]*\ssrc\s*=\s*"([^">]+)"[^>]*>|<img[^>]*\s(?:data-[^=>]*|src)=\s*['"]([^'">]*)['"][^>]*>`)

// normalizeEscapedLineBreaks 字面量 \r\n、\n、\r 转真实换行。
func normalizeEscapedLineBreaks(text string) string {
	text = strings.ReplaceAll(text, "\\r\\n", "\n")
	text = strings.ReplaceAll(text, "\\n", "\n")
	text = strings.ReplaceAll(text, "\\r", "\n")
	return text
}

// Format 正文格式化：块级标签转行、去标签、段落缩进（两个全角空格）。
func Format(html string) string {
	return formatText(html, "　　", false)
}

// FormatIntro 简介格式化（无缩进）。
func FormatIntro(html string) string {
	return formatText(html, "", false)
}

// FormatKeepImg 正文格式化但保留并重写 <img>（src 绝对化 + ,{...} 参数保留）。
func FormatKeepImg(html string, redirectURL string) string {
	kept := formatText(html, "　　", true)

	var sb strings.Builder
	appendPos := 0
	locs := formatImageRe.FindAllStringSubmatchIndex(kept, -1)
	for _, loc := range locs {
		var rawSrc string
		// 组 1: src 含 ,{...} 参数；组 2: data-src 等；组 3: src；组 4: 其他 data-*
		for _, g := range []int{1, 2, 3, 4} {
			if 2*g+1 < len(loc) && loc[2*g] >= 0 {
				rawSrc = kept[loc[2*g]:loc[2*g+1]]
				break
			}
		}
		param := ""
		urlPart := rawSrc
		// src 中 ,{...} 参数分离（legado AnalyzeUrl.paramPattern 语义）
		if bi := strings.Index(rawSrc, "{"); bi > 0 {
			// 向前找逗号（允许空白）
			for j := bi - 1; j >= 0; j-- {
				c := rawSrc[j]
				if c == ',' {
					urlPart = rawSrc[:j]
					param = "," + rawSrc[j+1:]
					break
				}
				if c != ' ' && c != '\t' {
					break
				}
			}
		}
		abs := analyzer.AbsoluteURLString(redirectURL, urlPart)
		sb.WriteString(kept[appendPos:loc[0]])
		sb.WriteString(`<img src="` + abs + param + `">`)
		appendPos = loc[1]
	}
	if appendPos < len(kept) {
		sb.WriteString(kept[appendPos:])
	}
	return sb.String()
}

// formatText 核心格式化管线。
func formatText(s, paragraphIndent string, keepImg bool) string {
	s = normalizeEscapedLineBreaks(s)
	s = nbspRe.ReplaceAllString(s, " ")
	s = espRe.ReplaceAllString(s, " ")
	s = noPrintRe.ReplaceAllString(s, "")
	s = wrapRe.ReplaceAllString(s, "\n")
	s = commentRe.ReplaceAllString(s, "")
	s = tagRe.ReplaceAllStringFunc(s, func(m string) string {
		if keepImg && strings.HasPrefix(strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(m, "</"), "<")), "img") {
			return m
		}
		return ""
	})
	s = indent1Re.ReplaceAllString(s, "\n"+paragraphIndent)
	s = indent2Re.ReplaceAllString(s, paragraphIndent)
	s = lastRe.ReplaceAllString(s, "")
	return s
}
