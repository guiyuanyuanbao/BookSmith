package export

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/go-pdf/fpdf"
)

func TestPDFImageEmbed(t *testing.T) {
	content := "　　中文渲染测试段落。\n&lt;img src=&quot;http://rs.sfacg.com/web/novel/images/uploadpic/2020/02/c9c066b3-2b8e-45c5-a72b-775ae94179ec.jpg&quot;&gt;\n图片后正文。"
	var called int
	// 生成真实可解析的 1x1 PNG（fpdf 会完整解码，假数据会被拒）
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal("生成 PNG 失败: " + err.Error())
	}
	validPNG := pngBuf.Bytes()
	ctx := &ExportContext{
		Book: BookMeta{
			Name: "测试书", Author: "作者",
			Chapters: []ChapterMeta{{Index: 0, Number: 1, Title: "第一章", Content: content}},
		},
		ImageProvider: func(rawURL string) ([]byte, string, bool) {
			called++
			return validPNG, "image/png", true
		},
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	// 字体
	fd, _, err := fontBytes(`C:\Windows\Fonts\simhei.ttf`)
	if err != nil {
		t.Skip("无字体")
	}
	pdf.AddUTF8FontFromBytes("cjk", "", fd)
	pdf.AddPage()
	pdf.SetFont("cjk", "", 11)
	// 手动执行与 Export 相同的行循环（提取成函数前的验证）
	imgSeq := 0
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if ctx.ImageProvider != nil && contentImgRe.MatchString(line) {
			if m := contentImgRe.FindStringSubmatch(line); m != nil {
				url := m[1]
				if url == "" {
					url = m[2]
				}
				t.Logf("匹配行: url=%q", url)
				if data, mime, ok := ctx.ImageProvider(url); ok {
					imgSeq++
					tp := fpdf.ImageOptions{ImageType: strings.TrimPrefix(extFromMime(mime), ".")}
					info := pdf.RegisterImageOptionsReader("t"+string(rune('0'+imgSeq)), tp, strings.NewReader(string(data)))
					if pdf.Err() {
						t.Errorf("RegisterImageOptionsReader 失败: %v", pdf.Error())
						continue
					}
					_ = info
					continue
				}
			}
		}
	}
	t.Logf("ImageProvider 调用次数=%d, 嵌入=%d", called, imgSeq)
	if called == 0 {
		t.Fatal("ImageProvider 从未被调用——插图分支未触发")
	}
	if imgSeq == 0 {
		t.Fatal("图片未注册")
	}
}
