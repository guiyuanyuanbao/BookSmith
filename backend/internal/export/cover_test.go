package export

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 有 CoverURL 时 EPUB 应嵌入真实图片 + OPF cover-image 属性。
func TestEPUBCoverEmbedded(t *testing.T) {
	// 模拟封面图（最小 PNG）
	pngBytes := []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n', // signature
		0, 0, 0, 13, 'I', 'H', 'D', 'R', // IHDR
		0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1F, 0x15, 0xC4, 0x89, // 1x1 RGBA
		0, 0, 0, 10, 'I', 'D', 'A', 'T', 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 防盗链：无 Referer 时 403
		if r.Header.Get("Referer") == "" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	ctx := testContext()
	ctx.Book.CoverURL = srv.URL + "/cover.png"
	var buf bytes.Buffer
	if err := (&EPUBExporter{}).Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	// 图片文件存在
	var imgFound bool
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "OEBPS/images/cover") {
			imgFound = true
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			if len(data) != len(pngBytes) {
				t.Errorf("image size = %d want %d", len(data), len(pngBytes))
			}
		}
	}
	if !imgFound {
		t.Error("cover image not embedded")
	}
	// OPF cover-image 属性
	opfFile := findFile(zr, "OEBPS/content.opf")
	opf := readFileString(t, opfFile)
	if !strings.Contains(opf, `properties="cover-image"`) {
		t.Error("OPF missing cover-image property")
	}
	if !strings.Contains(opf, "images/cover.png") {
		t.Error("OPF missing image item")
	}
	// cover.xhtml 引用图片
	coverX := readFileString(t, findFile(zr, "OEBPS/cover.xhtml"))
	if !strings.Contains(coverX, `<img src="images/cover.png"`) {
		t.Errorf("cover.xhtml missing img: %s", coverX[:min(300, len(coverX))])
	}
}

// 无 CoverURL 时退化为文字封面（不报错）。
func TestEPUBCoverFallback(t *testing.T) {
	ctx := testContext()
	ctx.Book.CoverURL = ""
	var buf bytes.Buffer
	if err := (&EPUBExporter{}).Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "OEBPS/images/") {
			t.Error("should not embed image without CoverURL")
		}
	}
	// 文字封面存在
	coverX := readFileString(t, findFile(zr, "OEBPS/cover.xhtml"))
	if !strings.Contains(coverX, xmlEscapeText(ctx.Book.Name)) {
		t.Error("text cover missing title")
	}
}

// 封面 URL 不可达时退化为文字封面（不中断导出）。
func TestEPUBCoverUnreachable(t *testing.T) {
	ctx := testContext()
	ctx.Book.CoverURL = "http://127.0.0.1:1/unreachable.png" // 端口 1 必然失败
	var buf bytes.Buffer
	if err := (&EPUBExporter{}).Export(ctx, &buf); err != nil {
		t.Fatalf("unreachable cover should not break export: %v", err)
	}
	if findFile(zrOf(t, buf), "OEBPS/images/cover.png") != nil {
		t.Error("should not embed failed image")
	}
}

// PDF：封面 URL 不可达/图片无效时不崩溃，正常生成 PDF（退化为文字封面）。
func TestPDFCoverFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 返回无效图片数据（fpdf 无法解码 → RegisterImageOptionsReader 失败 → 文字封面）
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00})
	}))
	defer srv.Close()

	ctx := testContext()
	ctx.Book.CoverURL = srv.URL + "/cover.jpg"
	ctx.FontPath = ResolveFontPath("")
	var buf bytes.Buffer
	if err := (&PDFExporter{}).Export(ctx, &buf); err != nil {
		t.Fatalf("invalid image should not break export: %v", err)
	}
	data := buf.Bytes()
	if !bytes.HasPrefix(data, []byte("%PDF-1.")) {
		t.Error("not a pdf")
	}
	if !bytes.Contains(data[len(data)-64:], []byte("%%EOF")) {
		t.Error("no EOF")
	}
}

// DownloadCover 双协议头修正 + Referer 注入。
func TestDownloadCoverURLFix(t *testing.T) {
	var gotReferer, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8})
	}))
	defer srv.Close()

	// 双协议头：https:https://127.0.0.1:port/img.jpg → https://127.0.0.1:port/img.jpg
	img := DownloadCover("https:" + srv.URL + "/img.jpg")
	if img == nil {
		t.Fatal("double-scheme cover should download")
	}
	if gotPath != "/img.jpg" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.HasPrefix(gotReferer, "http://127.0.0.1") {
		t.Errorf("referer = %q want site root", gotReferer)
	}
	// 空串
	if DownloadCover("") != nil {
		t.Error("empty url should return nil")
	}
	// 非 http
	if DownloadCover("javascript:void(0)") != nil {
		t.Error("javascript url should return nil")
	}
}

func findFile(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func zrOf(t *testing.T, buf bytes.Buffer) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

func readFileString(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	return string(data)
}

func xmlEscapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
