package analyzer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xpath"
	"golang.org/x/net/html"
)

// AnalyzeByXPath 是 legado AnalyzeByXPath.kt 的移植（antchfx/xpath 替代 JXDocument）。
type AnalyzeByXPath struct {
	node *html.Node
}

// NewAnalyzeByXPath 从内容构建。v: *html.Node / HTMLDoc / string。
func NewAnalyzeByXPath(v any) (*AnalyzeByXPath, error) {
	switch c := v.(type) {
	case *html.Node:
		if c == nil {
			return nil, errors.New("xpath: nil node")
		}
		return &AnalyzeByXPath{node: c}, nil
	case HTMLDoc:
		return NewAnalyzeByXPath(c.Node)
	case *HTMLDoc:
		if c == nil {
			return nil, errors.New("xpath: nil doc")
		}
		return NewAnalyzeByXPath(c.Node)
	case string:
		return NewAnalyzeByXPath(ParseContent(c))
	}
	return NewAnalyzeByXPath(ParseContent(fmt.Sprintf("%v", v)))
}

func (a *AnalyzeByXPath) getResult(xPath string) ([]*html.Node, error) {
	if a.node == nil {
		return nil, nil
	}
	// QueryAll 返回错误而非 panic（Find 会 panic）
	nodes, err := htmlquery.QueryAll(a.node, xPath)
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// GetElements 元素列表（含 && || %% 组合）。
func (a *AnalyzeByXPath) GetElements(xPath string) ([]*html.Node, error) {
	if xPath == "" {
		return nil, nil
	}
	ra := NewRuleAnalyzer(xPath, false)
	rules, err := ra.SplitRule("&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	if len(rules) == 1 {
		return a.getResult(rules[0])
	}
	var jxNodes []*html.Node
	var results [][]*html.Node
	for _, rl := range rules {
		temp, err := a.GetElements(rl)
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
						jxNodes = append(jxNodes, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				jxNodes = append(jxNodes, temp...)
			}
		}
	}
	return jxNodes, nil
}

// GetStringList 文本列表（含 && || %% 组合）。
func (a *AnalyzeByXPath) GetStringList(xPath string) ([]string, error) {
	result := []string{}
	ra := NewRuleAnalyzer(xPath, false)
	rules, err := ra.SplitRule("&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	if len(rules) == 1 {
		nodes, err := a.getResult(xPath)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			result = append(result, XPathNodeString(n))
		}
		return result, nil
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

// GetString 单值（多值 \n 连接；&& || 组合）。
func (a *AnalyzeByXPath) GetString(rule string) (string, error) {
	ra := NewRuleAnalyzer(rule, false)
	rules, err := ra.SplitRule("&&", "||")
	if err != nil {
		return "", err
	}
	if len(rules) == 1 {
		nodes, err := a.getResult(rule)
		if err != nil {
			return "", err
		}
		if len(nodes) == 0 {
			return "", nil
		}
		parts := make([]string, 0, len(nodes))
		for _, n := range nodes {
			parts = append(parts, XPathNodeString(n))
		}
		return strings.Join(parts, "\n"), nil
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

// XPathNodeString 等价 JXNode.asString：属性节点为合成元素（内含值文本），统一取内文。
func XPathNodeString(n *html.Node) string {
	if n == nil {
		return ""
	}
	return htmlquery.InnerText(n)
}

// CompileXPath 校验 XPath 表达式（书源调试用）。
func CompileXPath(expr string) (*xpath.Expr, error) {
	return xpath.Compile(expr)
}
