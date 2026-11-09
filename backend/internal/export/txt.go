package export

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"booksmith/internal/httpx"
)

// TXTExporter TXT 导出（元信息头 + 章节排版；可选 GBK）。
type TXTExporter struct{}

// Format 实现 Exporter。
func (e *TXTExporter) Format() Format { return FormatTXT }

// Ext 实现 Exporter。
func (e *TXTExporter) Ext() string { return "txt" }

// Export 实现 Exporter。
func (e *TXTExporter) Export(ctx *ExportContext, w io.Writer) error {
	bw := bufio.NewWriterSize(w, 1<<16)
	defer bw.Flush()

	// 元信息头
	intro := strings.TrimSpace(ctx.Book.Intro)
	if intro == "" {
		intro = "暂无"
	}
	intro = strings.ReplaceAll(intro, "\n", " ")
	if r := []rune(intro); len(r) > 200 {
		intro = string(r[:200])
	}
	fmt.Fprintf(bw, "书名：%s\n", ctx.Book.Name)
	fmt.Fprintf(bw, "作者：%s\n", ctx.Book.Author)
	fmt.Fprintf(bw, "简介：%s\n", intro)
	fmt.Fprintf(bw, "\n\n")

	curVolume := ""
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			curVolume = ch.Title
			fmt.Fprintf(bw, "\n\n════════ %s ════════\n\n", ch.Title)
			continue
		}
		// 章节标题
		fmt.Fprintf(bw, "\n\n%s\n\n", ch.Title)
		// 正文：段落两空格缩进
		for _, line := range strings.Split(ch.Content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			bw.WriteString("　　")
			bw.WriteString(line)
			bw.WriteString("\n")
		}
	}
	_ = curVolume
	return bw.Flush()
}

// TXTExportBytes 导出为字节（GBK 或 UTF-8）。
func TXTExportBytes(ctx *ExportContext, gbk bool) ([]byte, error) {
	var sb strings.Builder
	if err := (&TXTExporter{}).Export(ctx, &sb); err != nil {
		return nil, err
	}
	text := sb.String()
	if gbk {
		enc := httpx.EncodingByName("gbk")
		if enc == nil {
			return []byte(text), nil
		}
		out, err := enc.NewEncoder().String(text)
		if err != nil {
			// 含 GBK 外字符时逐字符替换
			return []byte(text), nil
		}
		return []byte(out), nil
	}
	return []byte(text), nil
}
