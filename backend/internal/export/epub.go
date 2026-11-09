package export

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// EPUBExporter EPUB 3 导出（原生 zip + OPF/NAV 实现，零外部依赖）。
type EPUBExporter struct{}

// Format 实现 Exporter。
func (e *EPUBExporter) Format() Format { return FormatEPUB }

// Ext 实现 Exporter。
func (e *EPUBExporter) Ext() string { return "epub" }

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func xmlEscape(s string) string { return xmlEscaper.Replace(s) }

var htmlTagStripRe = regexp.MustCompile(`<[^>]*>`)

// xhtmlEscapeBody 将正文纯文本转为 XHTML 段落。
func textToXHTML(content string) string {
	var sb strings.Builder
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sb.WriteString("<p>")
		sb.WriteString(xmlEscape(line))
		sb.WriteString("</p>\n")
	}
	return sb.String()
}

const epubMimetype = "application/epub+zip"

const epubContainerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

// Export 实现 Exporter：EPUB 3 结构（mimetype/META-INF/OEBPS/nav.xhtml/content.opf/章节）。
func (e *EPUBExporter) Export(ctx *ExportContext, w io.Writer) error {
	zw := zip.NewWriter(w)
	defer zw.Close()

	// 下载封面图（失败不中断导出，退化为文字封面）
	var cover *CoverImage
	if ctx.Book.CoverURL != "" {
		cover = DownloadCover(ctx.Book.CoverURL)
	}

	// mimetype 必须首位且不压缩
	mf, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		return err
	}
	if _, err := mf.Write([]byte(epubMimetype)); err != nil {
		return err
	}

	writeFile := func(name, data string) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, data)
		return err
	}

	// META-INF/container.xml
	if err := writeFile("META-INF/container.xml", epubContainerXML); err != nil {
		return err
	}

	bookID := uuid()
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	intro := strings.TrimSpace(htmlTagStripRe.ReplaceAllString(ctx.Book.Intro, ""))
	lang := "zh"

	// OEBPS/content.opf（EPUB 3）
	var opf strings.Builder
	opf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	opf.WriteString(`<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">` + "\n")
	opf.WriteString("  <metadata xmlns:dc=\"http://purl.org/dc/elements/1.1/\">\n")
	fmt.Fprintf(&opf, "    <dc:identifier id=\"bookid\">urn:uuid:%s</dc:identifier>\n", bookID)
	fmt.Fprintf(&opf, "    <dc:title>%s</dc:title>\n", xmlEscape(ctx.Book.Name))
	fmt.Fprintf(&opf, "    <dc:creator>%s</dc:creator>\n", xmlEscape(ctx.Book.Author))
	fmt.Fprintf(&opf, "    <dc:language>%s</dc:language>\n", lang)
	if intro != "" {
		fmt.Fprintf(&opf, "    <dc:description>%s</dc:description>\n", xmlEscape(intro))
	}
	fmt.Fprintf(&opf, "    <dc:publisher>BookSmith</dc:publisher>\n")
	fmt.Fprintf(&opf, "    <meta property=\"dcterms:modified\">%s</meta>\n", now)
	fmt.Fprintf(&opf, "    <meta name=\"generator\" content=\"BookSmith\"/>\n")
	opf.WriteString("  </metadata>\n")

	// manifest
	opf.WriteString("  <manifest>\n")
	opf.WriteString("    <item id=\"nav\" href=\"nav.xhtml\" media-type=\"application/xhtml+xml\" properties=\"nav\"/>\n")
	opf.WriteString("    <item id=\"ncx\" href=\"toc.ncx\" media-type=\"application/x-dtbncx+xml\"/>\n")
	opf.WriteString("    <item id=\"css\" href=\"style.css\" media-type=\"text/css\"/>\n")
	if cover != nil {
		fmt.Fprintf(&opf, "    <item id=\"cover-img\" href=\"images/cover%s\" media-type=\"%s\" properties=\"cover-image\"/>\n", cover.Ext, cover.MimeType)
	}
	opf.WriteString("    <item id=\"cover\" href=\"cover.xhtml\" media-type=\"application/xhtml+xml\"/>\n")
	chapterCount := 0
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			continue
		}
		chapterCount++
		fmt.Fprintf(&opf, "    <item id=\"ch%d\" href=\"chapters/ch%04d.xhtml\" media-type=\"application/xhtml+xml\"/>\n", ch.Number, ch.Number)
	}
	opf.WriteString("  </manifest>\n")

	// spine
	opf.WriteString("  <spine toc=\"ncx\">\n")
	opf.WriteString("    <itemref idref=\"cover\"/>\n")
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			continue
		}
		fmt.Fprintf(&opf, "    <itemref idref=\"ch%d\"/>\n", ch.Number)
	}
	opf.WriteString("  </spine>\n")
	opf.WriteString("</package>\n")
	if err := writeFile("OEBPS/content.opf", opf.String()); err != nil {
		return err
	}

	// style.css
	css := `body { font-family: serif; line-height: 1.6; margin: 1em; }
h1 { font-size: 1.4em; text-align: center; margin: 1.5em 0; }
h2 { font-size: 1.2em; text-align: center; margin: 2em 0 1em; color: #555; }
p { text-indent: 2em; margin: 0.4em 0; }
.cover { text-align: center; margin-top: 20%; }
.cover img { max-width: 100%; height: auto; box-shadow: 0 4px 12px rgba(0,0,0,0.3); }
`
	if err := writeFile("OEBPS/style.css", css); err != nil {
		return err
	}

	// cover.xhtml（有图显示图，无图显示文字）
	var coverPage string
	if cover != nil {
		coverPage = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>封面</title><link rel="stylesheet" type="text/css" href="style.css"/></head>
<body>
  <div class="cover">
    <img src="images/cover` + cover.Ext + `" alt="` + xmlEscape(ctx.Book.Name) + `" style="max-width:100%; max-height:90vh;"/>
  </div>
</body>
</html>
`
		// 写入图片文件
		imgFile, err := zw.Create("OEBPS/images/cover" + cover.Ext)
		if err != nil {
			return err
		}
		if _, err := imgFile.Write(cover.Data); err != nil {
			return err
		}
	} else {
		coverPage = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>封面</title><link rel="stylesheet" type="text/css" href="style.css"/></head>
<body>
  <div class="cover">
    <h1>` + xmlEscape(ctx.Book.Name) + `</h1>
    <p>` + xmlEscape(ctx.Book.Author) + `</p>
  </div>
</body>
</html>
`
	}
	if err := writeFile("OEBPS/cover.xhtml", coverPage); err != nil {
		return err
	}

	// nav.xhtml（EPUB 3 TOC）
	var nav strings.Builder
	nav.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	nav.WriteString(`<!DOCTYPE html>` + "\n")
	nav.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">` + "\n")
	nav.WriteString(`<head><title>目录</title></head>` + "\n")
	nav.WriteString("<body>\n  <nav epub:type=\"toc\" id=\"toc\">\n    <h1>目录</h1>\n    <ol>\n")
	// 卷分组
	nav.WriteString(`      <li><a href="cover.xhtml">封面</a></li>` + "\n")
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			fmt.Fprintf(&nav, "      <li><span>%s</span>\n        <ol>\n", xmlEscape(ch.Title))
			// 后续正文属于该卷
			for _, c2 := range ctx.Book.Chapters {
				if c2.IsVolume || c2.Volume != ch.Title {
					continue
				}
				fmt.Fprintf(&nav, "          <li><a href=\"chapters/ch%04d.xhtml\">%s</a></li>\n", c2.Number, xmlEscape(c2.Title))
			}
			nav.WriteString("        </ol>\n      </li>\n")
		}
	}
	// 无卷章节
	hasVolume := false
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			hasVolume = true
			break
		}
	}
	if !hasVolume {
		for _, ch := range ctx.Book.Chapters {
			fmt.Fprintf(&nav, "      <li><a href=\"chapters/ch%04d.xhtml\">%s</a></li>\n", ch.Number, xmlEscape(ch.Title))
		}
	}
	nav.WriteString("    </ol>\n  </nav>\n</body>\n</html>\n")
	if err := writeFile("OEBPS/nav.xhtml", nav.String()); err != nil {
		return err
	}

	// toc.ncx（兼容旧阅读器）
	var ncx strings.Builder
	ncx.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	ncx.WriteString(`<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">` + "\n<head>\n")
	fmt.Fprintf(&ncx, "  <meta name=\"dtb:uid\" content=\"urn:uuid:%s\"/>\n", bookID)
	ncx.WriteString("</head>\n<docTitle><text>" + xmlEscape(ctx.Book.Name) + "</text></docTitle>\n<navMap>\n")
	order := 0
	writeNavPoint := func(label, src string) {
		order++
		fmt.Fprintf(&ncx, "  <navPoint id=\"np%d\" playOrder=\"%d\"><navLabel><text>%s</text></navLabel><content src=\"%s\"/></navPoint>\n", order, order, xmlEscape(label), src)
	}
	writeNavPoint("封面", "cover.xhtml")
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			continue
		}
		writeNavPoint(ch.Title, fmt.Sprintf("chapters/ch%04d.xhtml", ch.Number))
	}
	ncx.WriteString("</navMap>\n</ncx>\n")
	if err := writeFile("OEBPS/toc.ncx", ncx.String()); err != nil {
		return err
	}

	// 章节 XHTML
	for _, ch := range ctx.Book.Chapters {
		if ch.IsVolume {
			continue
		}
		name := fmt.Sprintf("OEBPS/chapters/ch%04d.xhtml", ch.Number)
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		sb.WriteString(`<!DOCTYPE html>` + "\n")
		sb.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">` + "\n")
		sb.WriteString(`<head><title>` + xmlEscape(ch.Title) + `</title><link rel="stylesheet" type="text/css" href="../style.css"/></head>` + "\n")
		sb.WriteString("<body>\n")
		if ch.Volume != "" {
			fmt.Fprintf(&sb, "  <h2>%s</h2>\n", xmlEscape(ch.Volume))
		}
		fmt.Fprintf(&sb, "  <h1>%s</h1>\n", xmlEscape(ch.Title))
		sb.WriteString(textToXHTML(ch.Content))
		sb.WriteString("</body>\n</html>\n")
		if err := writeFile(name, sb.String()); err != nil {
			return err
		}
	}
	return nil
}

// uuid 生成随机 UUID v4。
func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

var _ = xml.EscapeText
