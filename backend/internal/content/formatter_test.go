package content

import (
	"strings"
	"testing"
)

func TestFormatBasic(t *testing.T) {
	in := `<div>第一段</div><p>&nbsp;第二段&nbsp;&nbsp;内容</p><br/>第三段<!--注释--><span>内联</span>`
	got := Format(in)
	if !strings.Contains(got, "　　第一段") {
		t.Errorf("indent missing: %q", got)
	}
	if strings.Contains(got, "&nbsp;") {
		t.Errorf("nbsp not replaced: %q", got)
	}
	if strings.Contains(got, "注释") || strings.Contains(got, "span") {
		t.Errorf("comment/span not removed: %q", got)
	}
	if strings.Contains(got, "　　内联") {
		t.Errorf("inline span should join previous line: %q", got)
	}
}

func TestFormatIntro(t *testing.T) {
	got := FormatIntro("<p>简介第一行</p><p>简介第二行</p>")
	if strings.Contains(got, "　　") {
		t.Errorf("intro should not indent: %q", got)
	}
	if !strings.Contains(got, "简介第一行") || !strings.Contains(got, "简介第二行") {
		t.Errorf("intro = %q", got)
	}
}

func TestFormatKeepImg(t *testing.T) {
	in := `<p>前文</p><img src="/img/cover.jpg" alt="x"/><p>后文</p>`
	got := FormatKeepImg(in, "https://a.com/book/")
	if !strings.Contains(got, `<img src="https://a.com/img/cover.jpg">`) {
		t.Errorf("img not absolutized: %q", got)
	}
	if !strings.Contains(got, "前文") || !strings.Contains(got, "后文") {
		t.Errorf("text lost: %q", got)
	}
	// data-src 优先
	in2 := `<img data-src="https://cdn.com/2.jpg" src="https://cdn.com/1.jpg"/>`
	got2 := FormatKeepImg(in2, "")
	if !strings.Contains(got2, "2.jpg") {
		t.Errorf("data-src priority: %q", got2)
	}
	// src 含 ,{...} 参数保留
	in3 := `<img src="https://cdn.com/3.jpg,{"option":"large"}"/>`
	got3 := FormatKeepImg(in3, "")
	if !strings.Contains(got3, `https://cdn.com/3.jpg,{"option":"large"}`) {
		t.Errorf("param keep: %q", got3)
	}
}

func TestFormatEscapedBreaks(t *testing.T) {
	// 字面量 \r\n 转真实换行
	got := Format(`<p>段落A\r\n段落B</p>`)
	if !strings.Contains(got, "段落A") || !strings.Contains(got, "段落B") {
		t.Errorf("escaped breaks: %q", got)
	}
}
