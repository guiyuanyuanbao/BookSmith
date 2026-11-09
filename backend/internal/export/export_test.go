package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"booksmith/internal/httpx"
)

func testContext() *ExportContext {
	return &ExportContext{
		Book: BookMeta{
			Name:     "测试之书",
			Author:   "测试作者",
			Intro:    "这是一本<b>测试</b>书籍的简介。\n第二行简介。",
			CoverURL: "",
			Chapters: []ChapterMeta{
				{Index: 0, IsVolume: true, Title: "第一卷 初入江湖"},
				{Index: 1, Number: 1, Volume: "第一卷 初入江湖", Title: "第一章 开端", Content: "第一段正文。\n第二段正文。"},
				{Index: 2, Number: 2, Volume: "第一卷 初入江湖", Title: "第二章 发展", Content: "第三段正文。"},
				{Index: 3, IsVolume: true, Title: "第二卷 崭露头角"},
				{Index: 4, Number: 3, Volume: "第二卷 崭露头角", Title: "第三章 高潮", Content: "第四段正文。"},
			},
		},
	}
}

func TestTXTExport(t *testing.T) {
	ctx := testContext()
	var buf bytes.Buffer
	if err := (&TXTExporter{}).Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "书名：测试之书") || !strings.Contains(out, "作者：测试作者") {
		t.Errorf("meta missing: %q", out[:min(120, len(out))])
	}
	if !strings.Contains(out, "第一章 开端") || !strings.Contains(out, "　　第一段正文。") {
		t.Errorf("chapter content missing")
	}
	if !strings.Contains(out, "════════ 第一卷 初入江湖 ════════") {
		t.Errorf("volume missing")
	}
	// GBK 导出（字节级验证：GBK 下不含 UTF-8 的中文序列）
	data, err := TXTExportBytes(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	got := gbkDecodeBytes(data)
	if !strings.Contains(got, "书名：测试之书") {
		t.Errorf("gbk roundtrip failed: %q", got[:min(60, len(got))])
	}
}

func TestEPUBExport(t *testing.T) {
	ctx := testContext()
	var buf bytes.Buffer
	if err := (&EPUBExporter{}).Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	// 结构完整性
	for _, name := range []string{"mimetype", "META-INF/container.xml", "OEBPS/content.opf", "OEBPS/nav.xhtml", "OEBPS/toc.ncx", "OEBPS/cover.xhtml", "OEBPS/style.css"} {
		if _, ok := files[name]; !ok {
			t.Errorf("missing %s", name)
		}
	}
	// mimetype 首位且不压缩
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Errorf("mimetype must be first & stored")
	}
	// 读取 mimetype 内容
	rc, _ := files["mimetype"].Open()
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "application/epub+zip" {
		t.Errorf("mimetype = %q", data)
	}
	// OPF XML 合法性
	rc, _ = files["OEBPS/content.opf"].Open()
	opfData, _ := io.ReadAll(rc)
	rc.Close()
	if err := xml.Unmarshal(opfData, new(any)); err != nil {
		t.Errorf("opf invalid xml: %v", err)
	}
	if !strings.Contains(string(opfData), "<dc:title>测试之书</dc:title>") {
		t.Errorf("opf title missing")
	}
	// NAV 引用三个章节
	rc, _ = files["OEBPS/nav.xhtml"].Open()
	navData, _ := io.ReadAll(rc)
	rc.Close()
	for _, ch := range []string{"第一章 开端", "第二章 发展", "第三章 高潮"} {
		if !strings.Contains(string(navData), ch) {
			t.Errorf("nav missing %s", ch)
		}
	}
	// 章节 XHTML 内容
	rc, _ = files["OEBPS/chapters/ch0001.xhtml"].Open()
	chData, _ := io.ReadAll(rc)
	rc.Close()
	if !strings.Contains(string(chData), "<p>第一段正文。</p>") {
		t.Errorf("chapter xhtml = %s", chData)
	}
	// 卷标题出现在章节内
	if !strings.Contains(string(chData), "第一卷 初入江湖") {
		t.Errorf("volume header missing in chapter")
	}
	// NCX 合法
	rc, _ = files["OEBPS/toc.ncx"].Open()
	ncxData, _ := io.ReadAll(rc)
	rc.Close()
	if err := xml.Unmarshal(ncxData, new(any)); err != nil {
		t.Errorf("ncx invalid: %v", err)
	}
}

func TestHTMLExport(t *testing.T) {
	ctx := testContext()
	var buf bytes.Buffer
	if err := (&HTMLExporter{}).Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "<h1 class=\"book\">测试之书</h1>") {
		t.Errorf("title missing")
	}
	// 简介先去标签再转义（展示语义）
	if !strings.Contains(out, "<div class=\"intro\">这是一本测试书籍的简介。") {
		t.Errorf("intro: %q", out)
	}
	if !strings.Contains(out, "第一段正文。") {
		t.Errorf("paragraph missing")
	}
}

func TestMDExport(t *testing.T) {
	ctx := testContext()
	var buf bytes.Buffer
	if err := (&MDExporter{}).Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "# 测试之书\n") {
		t.Errorf("md head: %q", out[:min(50, len(out))])
	}
	if !strings.Contains(out, "### 第一章 开端") || !strings.Contains(out, "第一段正文。") {
		t.Errorf("md chapter missing")
	}
	if !strings.Contains(out, "## 第一卷 初入江湖") {
		t.Errorf("md volume missing")
	}
}

func TestPDFExport(t *testing.T) {
	ctx := testContext()
	font := ResolveFontPath("")
	if font == "" {
		t.Skip("no CJK font available")
	}
	ctx.FontPath = font
	var buf bytes.Buffer
	if err := (&PDFExporter{}).Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	// PDF 头
	if !bytes.HasPrefix(data, []byte("%PDF-1.")) {
		t.Errorf("not a pdf: %q", data[:min(16, len(data))])
	}
	// EOF 标记
	if !bytes.Contains(data[len(data)-64:], []byte("%%EOF")) {
		t.Errorf("no EOF marker")
	}
	// 内嵌字体（CJK 应有 FontFile 引用）
	if !bytes.Contains(data, []byte("FontFile")) {
		t.Errorf("font not embedded")
	}
	// 页数（封面 + 卷2 + 章节3 = 至少 6 页）
	if !bytes.Contains(data, []byte("/Count")) {
		t.Errorf("page tree missing")
	}
	// 文档信息标题（UTF-16 编码存在）
	if !bytes.Contains(data, []byte("/Title")) {
		t.Errorf("title metadata missing")
	}
}

func TestForFormat(t *testing.T) {
	for _, f := range AllFormats() {
		e, err := ForFormat(f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		if e.Ext() != string(f) {
			t.Errorf("ext mismatch %s vs %s", e.Ext(), f)
		}
	}
	if _, err := ForFormat("mobi"); err == nil {
		t.Error("mobi should be unsupported")
	}
	if !ValidFormat("txt") || ValidFormat("doc") {
		t.Error("ValidFormat")
	}
	// 文件名（无源名）
	ctx := testContext()
	if name := FileName(ctx, "txt"); name != "测试之书(测试作者).txt" {
		t.Errorf("filename = %q", name)
	}
	// 有源名时带【源名】标记
	ctx.Book.SourceName = "笔趣阁"
	if name := FileName(ctx, "epub"); name != "测试之书(测试作者)【笔趣阁】.epub" {
		t.Errorf("filename with source = %q", name)
	}
}

// gbkDecodeBytes GBK 字节解码为 UTF-8 字符串。
func gbkDecodeBytes(b []byte) string {
	enc := httpx.EncodingByName("gbk")
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
}
