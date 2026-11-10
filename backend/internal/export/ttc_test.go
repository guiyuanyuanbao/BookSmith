package export

import (
	"encoding/binary"
	"os"
	"testing"
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
		rec := out[16+16*i : 16+16*(i+1)]
		off := int(binary.BigEndian.Uint32(rec[8:12]))
		length := int(binary.BigEndian.Uint32(rec[12:16]))
		if off < 12+16*numTables || off+length > len(out) {
			t.Errorf("表 %s 偏移/长度非法: off=%d len=%d size=%d", rec[0:4], off, length, len(out))
		}
	}
	// 关键验收：cmap 表存在且长度合理（此前 panic 正是 cmap 解析越界）
	hasCmap := false
	for i := 0; i < numTables; i++ {
		rec := out[16+16*i : 16+16*(i+1)]
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
