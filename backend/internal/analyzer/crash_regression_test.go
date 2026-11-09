package analyzer

import (
	"fmt"
	"testing"
)

// 复现真实书源触发的崩溃：{$.x} 内嵌规则解析为空时 pos 越过末尾（panic: slice bounds out of range）。
// 场景：非 JSON 根（字符串）+ JSON 规则 → fr 恒返回空 → 越界推进。
func TestInnerRuleEmptyResultNoOverrun(t *testing.T) {
	a, err := NewAnalyzeByJSONPath("plain text root, not json")
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"{$.a}&&{$.bc}",       // 组合规则（崩溃栈中的形态）
		"{$.a}",               // 单规则
		"{$.a}{$.b}{$.c}",     // 连续内嵌
		"{$.a}\\",             // 平衡组后跟反斜杠
		"{$.a\\",              // 尾部转义残缺
		"{$",                  // 残缺起始
		"{$.x.y}&&{$.z}&more", // 混合文本
	}
	for _, rule := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on rule %q: %v", rule, r)
				}
			}()
			got, _ := a.GetString(rule)
			fmt.Printf("rule=%q → %q\n", rule, got)
		}()
	}
}

// chompCodeBalanced 尾部反斜杠不得越界。
func TestChompTrailingEscape(t *testing.T) {
	ra := NewRuleAnalyzer("", true)
	for _, q := range []string{"{$.a\\", "{a\\", "{\\", "\\", "{$.a\\"} {
		ra.queue = q
		ra.pos = 0
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on queue %q: %v", q, r)
				}
			}()
			ra.chompCodeBalanced('{', '}')
		}()
	}
}

// 规则平衡组（jsoup/xpath 语义）同样不得越界。
func TestChompRuleTrailingEscape(t *testing.T) {
	ra := NewRuleAnalyzer("", false)
	for _, q := range []string{"[a\\", "[\\", "\\", "[a[b]\\"} {
		ra.queue = q
		ra.pos = 0
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on queue %q: %v", q, r)
				}
			}()
			ra.chompRuleBalanced('[', ']')
		}()
	}
}
