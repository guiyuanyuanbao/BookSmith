package analyzer

import (
	"fmt"
	"testing"
)

// 非 JSON 内容 + JSON 规则不得递归崩溃（真实书源触发过栈溢出）。
func TestJSONAnalyzerNonJSONContent(t *testing.T) {
	a, err := NewAnalyzeByJSONPath("<html>这不是JSON</html>")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := a.GetString("$.data.name")
	fmt.Printf("non-json root query = %q\n", got)
	list, _ := a.GetList("$.list[*]")
	if len(list) != 0 {
		t.Errorf("non-json root should match nothing, got %d", len(list))
	}
}

func TestJSONAnalyzerRulePrefixOnNonJSON(t *testing.T) {
	// 模拟真实场景：$. 规则作用于 HTML 内容（走 AnalyzeRule ModeJson 分支）
	ar := NewAnalyzeRule(nil, nil, nil)
	ar.SetContent("<html><body>plain html page</body></html>", "")
	got := ar.GetString("$.name")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
