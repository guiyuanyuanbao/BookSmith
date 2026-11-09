package export

import (
	"fmt"
	"io"
	"strings"
)

// HTMLExporter 单文件 HTML 导出。
type HTMLExporter struct{}

// Format 实现 Exporter。
func (e *HTMLExporter) Format() Format { return FormatHTML }

// Ext 实现 Exporter。
func (e *HTMLExporter) Ext() string { return "html" }

// Export 实现 Exporter。
func (e *HTMLExporter) Export(ctx *ExportContext, w io.Writer) error {
	var sb strings.Builder
	intro := strings.TrimSpace(htmlTagStripRe.ReplaceAllString(ctx.Book.Intro, ""))
	sb.WriteString(`<!DOCTYPE html>` + "\n")
	sb.WriteString(`<html lang="zh-CN">` + "\n<head>\n<meta charset=\"utf-8\"/>\n")
	fmt.Fprintf(&sb, "<title>%s - %s</title>\n", htmlEscapeText(ctx.Book.Name), htmlEscapeText(ctx.Book.Author))
	sb.WriteString(`<style>
body { max-width: 46em; margin: 0 auto; padding: 1em; line-height: 1.7; font-family: Georgia, 'Noto Serif SC', serif; color: #222; }
h1.book { text-align: center; margin-bottom: 0.2em; }
p.author { text-align: center; color: #666; margin-top: 0; }
div.intro { background: #f6f6f6; padding: 1em; border-radius: 8px; color: #444; margin: 1.5em 0; }
h1.ch { margin-top: 2.5em; padding-bottom: 0.3em; border-bottom: 1px solid #ddd; font-size: 1.3em; }
h2.vol { margin-top: 3em; text-align: center; color: #555; }
p { text-indent: 2em; margin: 0.5em 0; }
</style>
</head>
<body>
`)
	fmt.Fprintf(&sb, "<h1 class=\"book\">%s</h1>\n", htmlEscapeText(ctx.Book.Name))
	fmt.Fprintf(&sb, "<p class=\"author\">%s</p>\n", htmlEscapeText(ctx.Book.Author))
	if intro != "" {
		fmt.Fprintf(&sb, "<div class=\"intro\">%s</div>\n", htmlEscapeText(intro))
	}
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			fmt.Fprintf(&sb, "<h2 class=\"vol\">%s</h2>\n", htmlEscapeText(ch.Title))
			continue
		}
		if ch.Volume != "" {
			// 卷名已在遍历中输出，此处仅章节
		}
		fmt.Fprintf(&sb, "<h1 class=\"ch\">%s</h1>\n", htmlEscapeText(ch.Title))
		for _, line := range strings.Split(ch.Content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fmt.Fprintf(&sb, "<p>%s</p>\n", htmlEscapeText(line))
		}
	}
	sb.WriteString("</body>\n</html>\n")
	_, err := io.WriteString(w, sb.String())
	return err
}

func htmlEscapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// MDExporter Markdown 导出。
type MDExporter struct{}

// Format 实现 Exporter。
func (e *MDExporter) Format() Format { return FormatMD }

// Ext 实现 Exporter。
func (e *MDExporter) Ext() string { return "md" }

// Export 实现 Exporter。
func (e *MDExporter) Export(ctx *ExportContext, w io.Writer) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", ctx.Book.Name)
	if ctx.Book.Author != "" {
		fmt.Fprintf(&sb, "**作者**：%s\n\n", ctx.Book.Author)
	}
	intro := strings.TrimSpace(htmlTagStripRe.ReplaceAllString(ctx.Book.Intro, ""))
	if intro != "" {
		fmt.Fprintf(&sb, "> %s\n\n", strings.ReplaceAll(intro, "\n", "\n> "))
	}
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			fmt.Fprintf(&sb, "\n## %s\n\n", ch.Title)
			continue
		}
		fmt.Fprintf(&sb, "### %s\n\n", ch.Title)
		sb.WriteString(ch.Content)
		sb.WriteString("\n\n")
	}
	_, err := io.WriteString(w, sb.String())
	return err
}
