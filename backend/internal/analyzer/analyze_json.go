package analyzer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"
)

// AnalyzeByJSONPath 是 legado AnalyzeByJSonPath.kt 的移植（ojg 提供 Jayway 风格 JSONPath）。
type AnalyzeByJSONPath struct {
	root any
}

// NewAnalyzeByJSONPath 从内容构建。v: 已解析 JSON 值 / JSON 字符串。
// 非 JSON 字符串以原始字符串为根（查询不命中），与 legado JsonPath.parse 失败→空结果一致，
// 绝不再递归解析（否则栈溢出）。
func NewAnalyzeByJSONPath(v any) (*AnalyzeByJSONPath, error) {
	switch c := v.(type) {
	case string:
		parsed := parseJSONLoose(c)
		if ps, ok := parsed.(string); ok {
			// 解析失败：字符串原样作为根，不再进入本构造器
			return &AnalyzeByJSONPath{root: ps}, nil
		}
		return &AnalyzeByJSONPath{root: parsed}, nil
	case []byte:
		return NewAnalyzeByJSONPath(string(c))
	case nil:
		return nil, errors.New("json: nil content")
	default:
		return &AnalyzeByJSONPath{root: c}, nil
	}
}

// parseJSONLoose 宽松解析 JSON（兼容单引号键、尾逗号等不规范写法，GSON 宽松模式语义）。
func parseJSONLoose(s string) any {
	s = strings.TrimSpace(s)
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	// 宽松模式：ojg 支持非严格 JSON
	if v, err := oj.ParseString(s); err == nil {
		return v
	}
	// 再退化：返回原字符串（调用方须避免对字符串再递归解析）
	return s
}

// IsJSONContent 判断字符串是否为 JSON（对象/数组开头）。
func IsJSONContent(s string) bool {
	t := strings.TrimSpace(s)
	if len(t) < 2 {
		return false
	}
	if t[0] == '{' && t[len(t)-1] == '}' || t[0] == '[' && t[len(t)-1] == ']' {
		return json.Valid([]byte(t))
	}
	return false
}

func jsonPathRead(root any, expr string) ([]any, error) {
	p, err := jp.ParseString(expr)
	if err != nil {
		return nil, fmt.Errorf("jsonpath %q: %w", expr, err)
	}
	return p.Get(root), nil
}

func jsonToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%.0f", x)
		}
		return fmt.Sprintf("%v", x)
	case bool:
		return fmt.Sprintf("%v", x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return string(b)
	}
}

// GetString 单值。组合 && ||；{$.xxx} 内嵌替换；多值 \n 连接。
func (a *AnalyzeByJSONPath) GetString(rule string) (string, error) {
	if rule == "" {
		return "", nil
	}
	ra := NewRuleAnalyzer(rule, true)
	rules, err := ra.SplitRule("&&", "||")
	if err != nil {
		return "", err
	}
	if len(rules) == 1 {
		ra.ReSetPos()
		result := ra.InnerRuleBalanced("{$.", 1, 1, func(inner string) string {
			v, _ := a.GetString(inner)
			return v
		})
		if result == "" {
			matches, err := jsonPathRead(a.root, rule)
			if err != nil || len(matches) == 0 {
				return "", nil
			}
			if len(matches) == 1 {
				if list, ok := matches[0].([]any); ok {
					parts := make([]string, 0, len(list))
					for _, item := range list {
						parts = append(parts, jsonToString(item))
					}
					return strings.Join(parts, "\n"), nil
				}
				return jsonToString(matches[0]), nil
			}
			parts := make([]string, 0, len(matches))
			for _, m := range matches {
				parts = append(parts, jsonToString(m))
			}
			return strings.Join(parts, "\n"), nil
		}
		return result, nil
	}
	var textList []string
	for _, rl := range rules {
		temp, err := a.GetString(rl)
		if err != nil {
			return "", err
		}
		if temp != "" {
			textList = append(textList, temp)
			if ra.elementsType == "||" {
				break
			}
		}
	}
	return strings.Join(textList, "\n"), nil
}

// GetStringList 列表。组合 && || %%。
func (a *AnalyzeByJSONPath) GetStringList(rule string) ([]string, error) {
	result := []string{}
	if rule == "" {
		return result, nil
	}
	ra := NewRuleAnalyzer(rule, true)
	rules, err := ra.SplitRule("&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	if len(rules) == 1 {
		ra.ReSetPos()
		st := ra.InnerRuleBalanced("{$.", 1, 1, func(inner string) string {
			v, _ := a.GetString(inner)
			return v
		})
		if st == "" {
			matches, err := jsonPathRead(a.root, rule)
			if err != nil || len(matches) == 0 {
				return result, nil
			}
			if len(matches) == 1 {
				if list, ok := matches[0].([]any); ok {
					for _, item := range list {
						result = append(result, jsonToString(item))
					}
					return result, nil
				}
			}
			for _, m := range matches {
				result = append(result, jsonToString(m))
			}
			return result, nil
		}
		return append(result, st), nil
	}
	var results [][]string
	for _, rl := range rules {
		temp, err := a.GetStringList(rl)
		if err != nil {
			return nil, err
		}
		if len(temp) > 0 {
			results = append(results, temp)
			if ra.elementsType == "||" {
				break
			}
		}
	}
	if len(results) > 0 {
		if ra.elementsType == "%%" {
			for i := 0; i < len(results[0]); i++ {
				for _, temp := range results {
					if i < len(temp) {
						result = append(result, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				result = append(result, temp...)
			}
		}
	}
	return result, nil
}

// GetObject 取对象（getElement 用）。
func (a *AnalyzeByJSONPath) GetObject(rule string) (any, error) {
	matches, err := jsonPathRead(a.root, rule)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errors.New("jsonpath 未命中: " + rule)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return matches, nil
}

// GetList 取列表（getElements 用）。
func (a *AnalyzeByJSONPath) GetList(rule string) ([]any, error) {
	result := []any{}
	if rule == "" {
		return result, nil
	}
	ra := NewRuleAnalyzer(rule, true)
	rules, err := ra.SplitRule("&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	if len(rules) == 1 {
		matches, err := jsonPathRead(a.root, rules[0])
		if err != nil {
			return result, nil
		}
		if len(matches) == 1 {
			if list, ok := matches[0].([]any); ok {
				return list, nil
			}
		}
		return matches, nil
	}
	var results [][]any
	for _, rl := range rules {
		temp, err := a.GetList(rl)
		if err != nil {
			return nil, err
		}
		if len(temp) > 0 {
			results = append(results, temp)
			if ra.elementsType == "||" {
				break
			}
		}
	}
	if len(results) > 0 {
		if ra.elementsType == "%%" {
			for i := 0; i < len(results[0]); i++ {
				for _, temp := range results {
					if i < len(temp) {
						result = append(result, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				result = append(result, temp...)
			}
		}
	}
	return result, nil
}
