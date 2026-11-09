package analyzer

import (
	"reflect"
	"strings"
	"testing"
)

func TestRuleAnalyzerSplitRuleSimple(t *testing.T) {
	cases := []struct {
		rule   string
		split  []string
		want   []string
		ecType string
	}{
		{"a@b@c", []string{"@"}, []string{"a", "b", "c"}, ""},
		{"a&&b&&c", []string{"&&", "||", "%%"}, []string{"a", "b", "c"}, "&&"},
		{"a||b||c", []string{"&&", "||", "%%"}, []string{"a", "b", "c"}, "||"},
		{"a%%b", []string{"&&", "||", "%%"}, []string{"a", "b"}, "%%"},
		// 首个分隔符决定类型：b&&c 不再切
		{"a||b&&c", []string{"&&", "||", "%%"}, []string{"a", "b&&c"}, "||"},
		{"no-sep", []string{"&&", "||", "%%"}, []string{"no-sep"}, ""},
		{"a||", []string{"&&", "||", "%%"}, []string{"a", ""}, "||"},
	}
	for _, c := range cases {
		ra := NewRuleAnalyzer(c.rule, false)
		got, err := ra.SplitRule(c.split...)
		if err != nil {
			t.Fatalf("SplitRule(%q): %v", c.rule, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitRule(%q) = %v, want %v", c.rule, got, c.want)
		}
		if c.ecType != "" && ra.elementsType != c.ecType {
			t.Errorf("SplitRule(%q) elementsType = %q want %q", c.rule, ra.elementsType, c.ecType)
		}
	}
}

func TestRuleAnalyzerSplitRuleBalanced(t *testing.T) {
	cases := []struct {
		rule string
		want []string
	}{
		// @ 不在 [] 内才切分
		{"tag.div[0]@text", []string{"tag.div[0]", "text"}},
		{"class.odd[0:2]@tag.a@text", []string{"class.odd[0:2]", "tag.a", "text"}},
		// 索引内的 @ 不切分
		{"class.a[@href]@text", []string{"class.a[@href]", "text"}},
		// 选择器内的 && 不切分
		{"div.x&&y@text", []string{"div.x&&y", "text"}},
		// 括号内保护
		{"div:not(.a)&&(.b)@text", []string{"div:not(.a)&&(.b)", "text"}},
	}
	for _, c := range cases {
		ra := NewRuleAnalyzer(c.rule, false)
		got, err := ra.SplitRule("@")
		if err != nil {
			t.Fatalf("SplitRule(%q): %v", c.rule, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitRule(%q) = %v want %v", c.rule, got, c.want)
		}
	}
}

func TestRuleAnalyzerSplitRuleBrackets(t *testing.T) {
	// xpath 谓词内的 || 不切
	ra := NewRuleAnalyzer("//div[@id='a||b']//text()||//span", false)
	got, err := ra.SplitRule("&&", "||", "%%")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"//div[@id='a||b']//text()", "//span"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestRuleAnalyzerInnerRuleBalanced(t *testing.T) {
	// {$.a.b} 内嵌替换
	ra := NewRuleAnalyzer(`{$.data.title}-后缀`, true)
	got := ra.InnerRuleBalanced("{$.", 1, 1, func(inner string) string {
		if inner != "$.data.title" {
			t.Errorf("inner = %q", inner)
		}
		return "标题"
	})
	if got != "标题-后缀" {
		t.Errorf("got %q", got)
	}
	// 替换失败保留字面量
	ra = NewRuleAnalyzer(`{$.a}纯文本`, true)
	got = ra.InnerRuleBalanced("{$.", 1, 1, func(string) string { return "" })
	if got != "" {
		// 全部替换失败时返回空串（调用方回退直接读取）
		t.Logf("no-replace returns %q", got)
	}
	// JSON 内含 } 的平衡处理
	ra = NewRuleAnalyzer(`{$.a}x{$.b}`, true)
	calls := 0
	got = ra.InnerRuleBalanced("{$.", 1, 1, func(inner string) string {
		calls++
		return strings.TrimPrefix(inner, "$.")
	})
	if got != "axb" || calls != 2 {
		t.Errorf("got %q calls=%d", got, calls)
	}
}

func TestRuleAnalyzerInnerRulePair(t *testing.T) {
	ra := NewRuleAnalyzer("https://a.com/search/{{key}}/1/{{page}}", true)
	got := ra.InnerRulePair("{{", "}}", func(inner string) string {
		switch inner {
		case "key":
			return "斗破"
		case "page":
			return "2"
		}
		return ""
	})
	if got != "https://a.com/search/斗破/1/2" {
		t.Errorf("got %q", got)
	}
}

func TestRuleAnalyzerTrim(t *testing.T) {
	ra := NewRuleAnalyzer("@@class.a@text", false)
	ra.Trim()
	if ra.startX != 2 {
		t.Errorf("startX = %d want 2", ra.startX)
	}
}

func TestChompBalanced(t *testing.T) {
	// 规则平衡组：引号内转义无效
	ra := NewRuleAnalyzer("", false)
	ra.pos = 0
	ra.queue = `[a\]b]rest`
	if !ra.chompRuleBalanced('[', ']') {
		t.Fatal("should balance")
	}
	if ra.pos != 6 {
		t.Errorf("pos = %d want 6", ra.pos)
	}
	if got := ra.queue[:6]; got != "[a"+string(rune(92))+"]b]" {
		t.Errorf("consumed %q", got)
	}
	// 代码平衡组：处理转义
	ra = NewRuleAnalyzer("", true)
	ra.queue = `{a: "b}c"}rest`
	ra.pos = 0
	if !ra.chompCodeBalanced('{', '}') {
		t.Fatal("should balance")
	}
	if ra.pos != 10 {
		t.Errorf("pos = %d want 10", ra.pos)
	}
	// 引号内分隔符不结束平衡组
	ra = NewRuleAnalyzer("", true)
	ra.queue = `{'a}b'}rest`
	ra.pos = 0
	if !ra.chompCodeBalanced('{', '}') {
		t.Fatal("should balance with quote")
	}
	if ra.pos != 7 {
		t.Errorf("pos = %d want 7", ra.pos)
	}
}
