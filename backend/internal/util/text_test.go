package util

import (
	"strings"
	"testing"
	"unicode/utf8"
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
	// 中文 3 字节/字：240 字节上限 → 80 个中文字（字节级截断语义）
	if got := SanitizeFileName("很长的名字" + strings.Repeat("长", 200)); len([]rune(got)) != 80 || len(got) > maxNameBytes {
		t.Errorf("long name should be truncated to ≤%d bytes (80 runes), got %d runes/%d bytes", maxNameBytes, len([]rune(got)), len(got))
	}
}

// TestSanitizeFileName_ByteLimit UTF-8 字节级截断：ext4 NAME_MAX=255，纯中文按 rune 截会超限
// （历史 bug：书源规则把简介正文抓进作者字段，几百字 → mkdir file name too long）。
func TestSanitizeFileName_ByteLimit(t *testing.T) {
	// 纯中文 300 字 = 900 字节 → 截到 ≤240 字节
	long := strings.Repeat("打", 300)
	got := SanitizeFileName(long)
	if len(got) > maxNameBytes {
		t.Fatalf("字节截断失败: %d > %d", len(got), maxNameBytes)
	}
	if len(got) < maxNameBytes-3 { // 应贴近上限（最后一个 rune 放不下才停）
		t.Fatalf("过度截断: %d 字节", len(got))
	}
	// emoji 4 字节/字
	emoji := strings.Repeat("📚", 200) // 800 字节
	if got := SanitizeFileName(emoji); len(got) > maxNameBytes {
		t.Fatalf("emoji 截断失败: %d > %d", len(got), maxNameBytes)
	}
	// 短名不受影响
	if got := SanitizeFileName("正常书名"); got != "正常书名" {
		t.Fatalf("短名被误改: %q", got)
	}
	// 截断结果是合法 UTF-8
	if !utf8.ValidString(got) {
		t.Fatal("截断结果应合法 UTF-8")
	}
}

// TestSafeBookFileName_ByteLimit 书名+作者拼接后组合结果也必须 ≤255 字节
// （历史 bug 第二段：各段 240B 截断后组合可达 482B，Linux 下仍 ENAMETOOLONG）。
func TestSafeBookFileName_ByteLimit(t *testing.T) {
	got := SafeBookFileName(strings.Repeat("书", 200), strings.Repeat("作", 300))
	if len(got) > maxNameBytes {
		t.Fatalf("组合名超限: %d 字节 > %d（ext4 NAME_MAX=255 会 ENAMETOOLONG）", len(got), maxNameBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("结果应合法 UTF-8")
	}
	// 短名不受影响
	if SafeBookFileName("凡人修仙传", "忘语") != "凡人修仙传(忘语)" {
		t.Fatal("短名组合被误改")
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
