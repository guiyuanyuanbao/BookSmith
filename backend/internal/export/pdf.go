package export

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/go-pdf/fpdf"
)

// PDFExporter PDF 导出（fpdf + CJK TTF 字体嵌入）。
// 页面排版对标 so-novel：章节分页、标题居中、2em 缩进、页码页眉。
type PDFExporter struct{}

// Format 实现 Exporter。
func (e *PDFExporter) Format() Format { return FormatPDF }

// Ext 实现 Exporter。
func (e *PDFExporter) Ext() string { return "pdf" }

// ResolveFontPath 解析可用的 CJK TTF 字体路径。
// 顺序：用户配置 → Windows → macOS → Linux 常见路径。
func ResolveFontPath(configured string) string {
	if configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{
			`C:\Windows\Fonts\simhei.ttf`,
			`C:\Windows\Fonts\simfang.ttf`,
			`C:\Windows\Fonts\simkai.ttf`,
			`C:\Windows\Fonts\NotoSansSC-VF.ttf`,
			`C:\Windows\Fonts\simsunb.ttf`,
		}
	case "darwin":
		candidates = []string{
			"/System/Library/Fonts/Supplemental/Songti.ttc",
			"/System/Library/Fonts/Supplemental/STHeiti Light.ttc",
			"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
		}
	default: // linux 等
		candidates = []string{
			"/usr/share/fonts/wqy-zenhei/wqy-zenhei.ttc",        // alpine font-wqy-zenhei
			"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",       // debian fonts-wqy-zenhei
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/arphic/uming.ttc",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// extractTTCFirstFont 从 TTC（TrueType Collection）中提取第一个字体为独立 sfnt 字体文件。
//
// 关键点：TTC 中每个字体的表目录里，offset/length 字段是相对**整个 TTC 文件**的绝对偏移。
// 因此不能简单切片 data[fontOffset:]——那样所有表偏移都会错位 TTC 头大小（24 字节），
// 导致 fpdf 解析 cmap/hhea 等表时读出垃圾偏移而 panic。
// 正确做法：解析首字体的表目录，把每张表的数据按新文件的偏移重新布局，重建表目录。
func extractTTCFirstFont(data []byte) ([]byte, error) {
	if len(data) < 12 || string(data[:4]) != "ttcf" {
		return nil, fmt.Errorf("非 TTC 文件")
	}
	numFonts := int(binary.BigEndian.Uint32(data[8:12]))
	if numFonts < 1 {
		return nil, fmt.Errorf("TTC 无字体")
	}
	fontOffset := int(binary.BigEndian.Uint32(data[12:16]))
	if fontOffset <= 0 || fontOffset+12 > len(data) {
		return nil, fmt.Errorf("TTC offset 非法")
	}

	// 解析首字体的 sfnt 表目录（记录中的 offset/length 相对整个 TTC 文件）
	numTables := int(binary.BigEndian.Uint16(data[fontOffset+4 : fontOffset+6]))
	if numTables == 0 || fontOffset+12+16*numTables > len(data) {
		return nil, fmt.Errorf("TTC 表目录越界")
	}
	type tableRec struct {
		tag      string
		checksum []byte // 4 字节，数据不变则校验和仍有效
		data     []byte
	}
	tables := make([]tableRec, 0, numTables)
	for i := 0; i < numTables; i++ {
		rec := data[fontOffset+12+16*i : fontOffset+12+16*(i+1)]
		tag := string(rec[0:4])
		off := int(binary.BigEndian.Uint32(rec[8:12]))
		length := int(binary.BigEndian.Uint32(rec[12:16]))
		if off < 0 || length < 0 || off+length > len(data) {
			return nil, fmt.Errorf("表 %s 越界（off=%d len=%d）", tag, off, length)
		}
		tables = append(tables, tableRec{tag: tag, checksum: append([]byte(nil), rec[4:8]...), data: data[off : off+length]})
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("TTC 无有效表")
	}

	// 重建独立字体文件：表目录按 tag 排序（sfnt 规范），数据 4 字节对齐顺序排列
	sort.Slice(tables, func(a, b int) bool { return tables[a].tag < tables[b].tag })

	dirSize := 16 + 16*len(tables) // sfnt 版本(4) + 头(12) = 16 字节总头，随后表目录
	totalSize := dirSize
	for _, t := range tables {
		totalSize += len(t.data)
		pad := (4 - len(t.data)%4) % 4
		totalSize += pad
	}
	out := make([]byte, 0, totalSize)

	// sfnt 头：版本取自该字体的目录（0x00010000 TrueType / 'OTTO' CFF）
	out = append(out, data[fontOffset:fontOffset+4]...)
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint16(hdr[0:2], uint16(len(tables)))
	entrySelector := 0
	for 1<<(entrySelector+1) <= len(tables) {
		entrySelector++
	}
	searchRange := (1 << entrySelector) * 16
	binary.BigEndian.PutUint16(hdr[2:4], uint16(searchRange))
	binary.BigEndian.PutUint16(hdr[4:6], uint16(entrySelector))
	binary.BigEndian.PutUint16(hdr[6:8], uint16(len(tables)*16-searchRange))
	out = append(out, hdr...)

	// 表目录：偏移改为相对新文件（rec 连续排列，pad 仅用于表数据对齐）
	dataStart := dirSize
	cursor := dataStart
	for _, t := range tables {
		rec := make([]byte, 16)
		copy(rec[0:4], t.tag)
		copy(rec[4:8], t.checksum)
		binary.BigEndian.PutUint32(rec[8:12], uint32(cursor))
		binary.BigEndian.PutUint32(rec[12:16], uint32(len(t.data)))
		out = append(out, rec...)
		pad := (4 - len(t.data)%4) % 4
		cursor += len(t.data) + pad
	}
	// 表数据（按目录中记录的偏移顺序写出）
	for _, t := range tables {
		out = append(out, t.data...)
		pad := (4 - len(t.data)%4) % 4
		out = append(out, make([]byte, pad)...)
	}
	return out, nil
}

// fontBytes 解析字体文件为字节集（TTC 自动提取首字体，TTF/OTF 原样返回）。
func fontBytes(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if len(data) >= 4 && string(data[:4]) == "ttcf" {
		sub, err := extractTTCFirstFont(data)
		if err != nil {
			return nil, false, err
		}
		return sub, true, nil
	}
	return data, false, nil
}

// Export 实现 Exporter。
func (e *PDFExporter) Export(ctx *ExportContext, w io.Writer) error {
	fontPath := ResolveFontPath(ctx.FontPath)

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 18, 20)
	pdf.SetAutoPageBreak(true, 18)
	pdf.SetTitle(ctx.Book.Name, true)
	pdf.SetAuthor(ctx.Book.Author, true)
	pdf.SetCreator("BookSmith", true)

	useUnicode := false
	if fontPath != "" {
		fontData, _, ferr := fontBytes(fontPath) // TTC 自动提取首字体
		if ferr == nil && len(fontData) > 0 {
			pdf.AddUTF8FontFromBytes("cjk", "", fontData)
			if pdf.Err() {
				return fmt.Errorf("加载字体 %s: %v", fontPath, pdf.Err())
			}
			useUnicode = true
		}
	}
	font := "cjk"
	if !useUnicode {
		// 无 CJK 字体时退化为内置 Helvetica（仅 ASCII 内容可用）
		font = "Helvetica"
	}

	// 封面页：优先绘制真实封面图，失败退化为文字封面（手动上传优先于远程下载）
	pdf.AddPage()
	var cover *CoverImage
	if ctx.Book.LocalCover != nil {
		cover = ctx.Book.LocalCover
	} else if ctx.Book.CoverURL != "" {
		if ctx.CoverProvider != nil {
			cover = ctx.CoverProvider(ctx.Book.CoverURL)
		} else {
			cover = DownloadCover(ctx.Book.CoverURL)
		}
	}
	if cover != nil && useUnicode {
		// 注册图片并绘制；fpdf 解码失败时回退文字封面
		imgName := "cover" + cover.Ext
		pdf.RegisterImageOptionsReader(imgName, fpdf.ImageOptions{ImageType: strings.TrimPrefix(cover.Ext, ".")}, bytes.NewReader(cover.Data))
		if !pdf.Err() {
			// A4: 210×297mm，图片居中等比缩放（宽至 150mm，高按比例）
			info := pdf.GetImageInfo(imgName)
			imgW, imgH := 150.0, 0.0
			if info != nil && info.Width() > 0 {
				imgH = imgW * info.Height() / info.Width()
			}
			if imgH > 240 {
				imgH = 240
				imgW = imgH * info.Width() / info.Height()
			}
			x := (210 - imgW) / 2
			y := (297 - imgH) / 2
			pdf.Image(imgName, x, y, imgW, imgH, false, "", 0, "")
		} else {
			pdf.ClearError()
			// 图片解码失败：文字封面
			pdf.SetFont(font, "", 26)
			pdf.SetY(90)
			pdf.CellFormat(0, 14, ctx.Book.Name, "", 1, "C", false, 0, "")
			pdf.SetFont(font, "", 14)
			pdf.Ln(6)
			pdf.CellFormat(0, 10, ctx.Book.Author, "", 1, "C", false, 0, "")
		}
	} else {
		pdf.SetFont(font, "", 26)
		pdf.SetY(90)
		pdf.CellFormat(0, 14, ctx.Book.Name, "", 1, "C", false, 0, "")
		pdf.SetFont(font, "", 14)
		pdf.Ln(6)
		pdf.CellFormat(0, 10, ctx.Book.Author, "", 1, "C", false, 0, "")
		intro := strings.TrimSpace(htmlTagStripRe.ReplaceAllString(ctx.Book.Intro, ""))
		if intro != "" && useUnicode {
			if r := []rune(intro); len(r) > 120 {
				intro = string(r[:120]) + "…"
			}
			pdf.SetY(130)
			pdf.SetFont(font, "", 10)
			pdf.MultiCell(0, 6, intro, "", "C", false)
		}
	}

	// 页眉页脚
	pdf.SetHeaderFuncMode(func() {
		if pdf.PageNo() <= 1 {
			return
		}
		pdf.SetFont(font, "", 8)
		pdf.SetTextColor(128, 128, 128)
		pdf.CellFormat(0, 6, ctx.Book.Name, "", 0, "C", false, 0, "")
		pdf.Ln(-1)
		pdf.SetTextColor(0, 0, 0)
	}, false)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-14)
		pdf.SetFont(font, "", 8)
		pdf.SetTextColor(128, 128, 128)
		pdf.CellFormat(0, 8, fmt.Sprintf("- %d -", pdf.PageNo()), "", 0, "C", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	})

	// 章节
	pdf.SetTextColor(0, 0, 0)
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			// 卷名：新页居中
			pdf.AddPage()
			pdf.SetFont(font, "", 18)
			pdf.SetY(120)
			pdf.CellFormat(0, 12, ch.Title, "", 1, "C", false, 0, "")
			continue
		}
		pdf.AddPage()
		pdf.SetFont(font, "", 15)
		pdf.CellFormat(0, 10, ch.Title, "", 1, "C", false, 0, "")
		pdf.Ln(3)
		if ch.Volume != "" {
			pdf.SetFont(font, "", 10)
			pdf.SetTextColor(110, 110, 110)
			pdf.CellFormat(0, 6, ch.Volume, "", 1, "C", false, 0, "")
			pdf.SetTextColor(0, 0, 0)
			pdf.Ln(2)
		}
		pdf.SetFont(font, "", 11)
		for _, line := range strings.Split(ch.Content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// 首行缩进两个全角空格
			if useUnicode {
				pdf.MultiCell(0, 6.2, "　　"+line, "", "", false)
			} else {
				pdf.MultiCell(0, 6.2, line, "", "", false)
			}
		}
	}
	return pdf.Output(w)
}

func fontExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' || path[i] == '\\' {
			break
		}
	}
	return ""
}
