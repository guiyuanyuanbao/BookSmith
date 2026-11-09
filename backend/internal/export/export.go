// Package export 实现多格式电子书导出：TXT / EPUB 3 / PDF / HTML / Markdown。
// 统一 Exporter 接口，章节内容复用（一次抓取多格式导出）。
package export

import (
	"fmt"
	"io"
)

// Format 导出格式。
type Format string

const (
	FormatTXT  Format = "txt"
	FormatEPUB Format = "epub"
	FormatPDF  Format = "pdf"
	FormatHTML Format = "html"
	FormatMD   Format = "md"
)

// AllFormats 支持的全部格式。
func AllFormats() []Format {
	return []Format{FormatTXT, FormatEPUB, FormatPDF, FormatHTML, FormatMD}
}

// ValidFormat 格式是否支持。
func ValidFormat(f string) bool {
	for _, v := range AllFormats() {
		if string(v) == f {
			return true
		}
	}
	return false
}

// ChapterMeta 章节元信息（卷分组）。
type ChapterMeta struct {
	Index    int    // 全局序号（0 起）
	Number   int    // 正文章序号（跳过卷）
	Volume   string // 所属卷名（"" 为无卷）
	IsVolume bool   // 本条是卷名
	Title    string
	Content  string // 已净化正文（段落以 \n 分隔）
}

// BookMeta 书籍元信息。
type BookMeta struct {
	Name       string
	Author     string
	Intro      string
	CoverURL   string
	// LocalCover 手动上传的封面（优先于 CoverURL）；nil 时回退远程下载
	LocalCover *CoverImage
	SourceName string // 书源名（导出文件名带源标记）
	Chapters   []ChapterMeta
}

// ExportContext 导出参数。
type ExportContext struct {
	Book     BookMeta
	TXTGBK   bool   // TXT 导出 GBK 编码
	FontPath string // PDF 字体（空则自动解析系统字体）
}

// Exporter 导出器接口。
type Exporter interface {
	Format() Format
	// Ext 文件扩展名（不含点）。
	Ext() string
	Export(ctx *ExportContext, w io.Writer) error
}

// ForFormat 获取格式导出器。
func ForFormat(f Format) (Exporter, error) {
	switch f {
	case FormatTXT:
		return &TXTExporter{}, nil
	case FormatEPUB:
		return &EPUBExporter{}, nil
	case FormatPDF:
		return &PDFExporter{}, nil
	case FormatHTML:
		return &HTMLExporter{}, nil
	case FormatMD:
		return &MDExporter{}, nil
	}
	return nil, fmt.Errorf("不支持的导出格式: %s", f)
}

// FileName 导出文件名：书名(作者)【源名】.ext；无源名时保持书名(作者).ext。
func FileName(ctx *ExportContext, ext string) string {
	name := ctx.Book.Name
	if ctx.Book.Author != "" {
		name = fmt.Sprintf("%s(%s)", name, ctx.Book.Author)
	}
	if ctx.Book.SourceName != "" {
		name = fmt.Sprintf("%s【%s】", name, ctx.Book.SourceName)
	}
	return fmt.Sprintf("%s.%s", name, ext)
}

// FormatFormats 格式列表转字符串。
func FormatFormats(fs []string) []Format {
	out := make([]Format, 0, len(fs))
	for _, f := range fs {
		if ValidFormat(f) {
			out = append(out, Format(f))
		}
	}
	return out
}
