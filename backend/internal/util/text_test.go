package util

import (
	"strings"
	"testing"
)

func TestCleanInvisibleChars(t *testing.T) {
	// PUA 区字符（E000-F8FF）是中文乱码根源，必须移除
	in := "正常\uE000文本\u200B还有\uFEFF零宽\u0001控制"
	got := CleanInvisibleChars(in)
	want := "正常文本还有零宽控制"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// 保留换行与制表
	if got := CleanInvisibleChars("a\nb\tc"); got != "a\nb\tc" {
		t.Fatalf("newline/tab must be preserved, got %q", got)
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"正常的书名", "正常的书名"},
		{`非/法:字*符?.`, "非法字符"},
		{"  空格  ", "空格"},
		{"CON", "_"},
		{"con", "_"},
		{"..a..", "a"},
		{"", "_"},
	}
	for _, c := range cases {
		if got := SanitizeFileName(c.in); got != c.want {
			t.Errorf("SanitizeFileName(%q)=%q want %q", c.in, got, c.want)
		}
	}
	if got := SanitizeFileName("很长的名字" + strings.Repeat("长", 200)); len([]rune(got)) != 100 {
		t.Errorf("long name should be truncated to 100 runes, got %d", len([]rune(got)))
	}
}

func TestSafeBookFileName(t *testing.T) {
	if got := SafeBookFileName("凡人修仙传", "忘语"); got != "凡人修仙传(忘语)" {
		t.Fatalf("got %q", got)
	}
	if got := SafeBookFileName("书", ""); got != "书" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeHTMLEntities(t *testing.T) {
	in := "&nbsp;&amp;&lt;p&gt;&#x4e2d;&#20013;"
	want := "\u00a0&<p>中中"
	if got := DecodeHTMLEntities(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// 不含 & 时应快速返回原字符串
	if got := DecodeHTMLEntities("abc"); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestRemoveBlankLines(t *testing.T) {
	in := "第一段\n\n\n第二段\n   \n第三段\n"
	want := "第一段\n第二段\n第三段"
	if got := RemoveBlankLines(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHalfWidth(t *testing.T) {
	if got := HalfWidth("１２３ＡＢｃ"); got != "123ABc" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeBookKey(t *testing.T) {
	a := NormalizeBookKey("斗破苍穹", "天蚕土豆")
	b := NormalizeBookKey("斗破 苍穹", "天蚕 土豆")
	if a != b {
		t.Fatalf("keys should match: %q vs %q", a, b)
	}
	c := NormalizeBookKey("斗破苍穹", "天蚕土荳")
	if a == c {
		t.Fatalf("different books should differ")
	}
}

func TestExtOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://a.com/x/y.JPG?a=1", "jpg"},
		{"/path/to/file.tar.gz#frag", "gz"},
		{"noext", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := ExtOf(c.in); got != c.want {
			t.Errorf("ExtOf(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
