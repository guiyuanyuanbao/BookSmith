package export

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/go-pdf/fpdf"
)

// TestExtractTTCFirstFont_RealTTC 用本机真实 TTC 验证提取产物的表目录自洽
// （CI 容器无 Windows 字体，自动跳过；本机诊断用）
func TestExtractTTCFirstFont_RealTTC(t *testing.T) {
	candidates := []string{
		`C:\Windows\Fonts\msyh.ttc`,
		`C:\Windows\Fonts\simhei.ttf`,
		"/usr/share/fonts/wqy-zenhei/wqy-zenhei.ttc",
	}
	var path string
	var isTTC bool
	for _, c := range candidates {
		data, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		path = c
		isTTC = len(data) >= 4 && string(data[:4]) == "ttcf"
		break
	}
	if path == "" {
		t.Skip("无可用字体文件")
	}
	data, _ := os.ReadFile(path)

	out, err := extractTTCFirstFont(data)
	if err != nil {
		if !isTTC {
			t.Skipf("非 TTC，无需提取: %v", err)
		}
		t.Fatal(err)
	}
	if !isTTC {
		t.Skipf("源为单字体文件（%s），TTC 提取仅对 TTC 触发", path)
	}

	// 验证提取产物：sfnt 头 + 表目录自洽（每张表的数据范围都在产物内）
	if len(out) < 12 {
		t.Fatal("产物过小")
	}
	numTables := int(binary.BigEndian.Uint16(out[4:6]))
	if numTables == 0 || 12+16*numTables > len(out) {
		t.Fatalf("表目录非法: numTables=%d size=%d", numTables, len(out))
	}
	for i := 0; i < numTables; i++ {
		rec := out[12+16*i : 12+16*(i+1)]
		off := int(binary.BigEndian.Uint32(rec[8:12]))
		length := int(binary.BigEndian.Uint32(rec[12:16]))
		if off < 12+16*numTables || off+length > len(out) {
			t.Errorf("表 %s 偏移/长度非法: off=%d len=%d size=%d", rec[0:4], off, length, len(out))
		}
	}
	// 关键验收：cmap 表存在且长度合理（此前 panic 正是 cmap 解析越界）
	hasCmap := false
	for i := 0; i < numTables; i++ {
		rec := out[12+16*i : 12+16*(i+1)]
		if string(rec[0:4]) == "cmap" {
			hasCmap = true
			off := int(binary.BigEndian.Uint32(rec[8:12]))
			length := int(binary.BigEndian.Uint32(rec[12:16]))
			if length == 0 || off+length > len(out) {
				t.Errorf("cmap 表异常: off=%d len=%d", off, length)
			}
		}
	}
	if !hasCmap {
		t.Error("缺少 cmap 表")
	}
}

// TestExtractTTCFirstFont_FpdfParses 以消费端为准：提取产物必须能被 fpdf 正常解析
// （此前 bug 的教训——自洽性测试按实现读取，测试通过但消费端必炸）
func TestExtractTTCFirstFont_FpdfParses(t *testing.T) {
	candidates := []string{
		`C:\Windows\Fonts\msyh.ttc`,
		"/usr/share/fonts/wqy-zenhei/wqy-zenhei.ttc",
	}
	var data []byte
	var path string
	for _, c := range candidates {
		d, err := os.ReadFile(c)
		if err == nil && len(d) >= 4 && string(d[:4]) == "ttcf" {
			data = d
			path = c
			break
		}
	}
	if data == nil {
		t.Skip("无 TTC 字体文件")
	}
	out, err := extractTTCFirstFont(data)
	if err != nil {
		t.Fatal(err)
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddUTF8FontFromBytes("cjk", "", out)
	if pdf.Err() {
		t.Fatalf("fpdf 解析提取产物失败: %v", pdf.Error())
	}
	pdf.AddPage()
	pdf.SetFont("cjk", "", 16)
	pdf.Text(20, 30, "中文渲染验证：来自深渊的我今天也要拯救人类")
	if pdf.Err() {
		t.Fatalf("中文文本写入失败: %v", pdf.Error())
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		t.Fatalf("PDF 输出失败: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("PDF 输出为空")
	}
	t.Logf("fpdf 解析并渲染成功: %s -> %d 字节 PDF", path, buf.Len())
}
