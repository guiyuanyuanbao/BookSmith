package analyzer

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// AnalyzeByJSoup 是 legado AnalyzeByJSoup.kt 的移植：
// 默认 jsoup 语法（class.x@tag.a@text）、@CSS: 前缀、|| && %% 组合、两种索引语法。
type AnalyzeByJSoup struct {
	element *html.Node
}

// NewAnalyzeByJSoup 从内容构建解析器。v 可为 *html.Node / HTMLDoc / string。
func NewAnalyzeByJSoup(v any) (*AnalyzeByJSoup, error) {
	switch c := v.(type) {
	case *html.Node:
		if c == nil {
			return nil, errors.New("jsoup: nil node")
		}
		return &AnalyzeByJSoup{element: c}, nil
	case HTMLDoc:
		if c.Node == nil {
			return nil, errors.New("jsoup: nil node")
		}
		return &AnalyzeByJSoup{element: c.Node}, nil
	case *HTMLDoc:
		if c == nil || c.Node == nil {
			return nil, errors.New("jsoup: nil node")
		}
		return &AnalyzeByJSoup{element: c.Node}, nil
	case string:
		return NewAnalyzeByJSoup(ParseContent(c))
	case []byte:
		return NewAnalyzeByJSoup(ParseContent(string(c)))
	}
	// 其他类型转字符串再解析
	return NewAnalyzeByJSoup(ParseContent(fmt.Sprintf("%v", v)))
}

// HTMLDoc 包装解析后的 HTML 节点（文档或元素）。
type HTMLDoc struct {
	Node *html.Node
	XML  bool // XML 文档（保留大小写）
}

// ParseContent 解析 HTML 字符串；以 <?xml 开头时按 XML 处理。
func ParseContent(content string) HTMLDoc {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<!") && strings.Contains(trimmed[:min(64, len(trimmed))], "xml") {
		if node := parseXMLDocument(trimmed); node != nil {
			return HTMLDoc{Node: node, XML: true}
		}
	}
	node, err := parseHTML(content)
	if err != nil || node == nil {
		return HTMLDoc{}
	}
	return HTMLDoc{Node: node}
}

// GetString 合并内容列表得到内容（多值以 \n 连接）。
func (a *AnalyzeByJSoup) GetString(ruleStr string) (string, error) {
	if ruleStr == "" {
		return "", nil
	}
	list, err := a.GetStringList(ruleStr)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", nil
	}
	if len(list) == 1 {
		return list[0], nil
	}
	return strings.Join(list, "\n"), nil
}

// GetString0 获取第一个字符串（URL 规则场景）。
func (a *AnalyzeByJSoup) GetString0(ruleStr string) (string, error) {
	list, err := a.GetStringList(ruleStr)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", nil
	}
	return list[0], nil
}

// jsoupSourceRule 对应 AnalyzeByJSoup.SourceRule。
type jsoupSourceRule struct {
	isCSS        bool
	elementsRule string
}

func parseJSoupSourceRule(ruleStr string) jsoupSourceRule {
	if len(ruleStr) >= 5 && strings.EqualFold(ruleStr[:5], "@CSS:") {
		return jsoupSourceRule{isCSS: true, elementsRule: strings.TrimSpace(ruleStr[5:])}
	}
	return jsoupSourceRule{elementsRule: ruleStr}
}

// GetStringList 获取所有内容列表（含组合符）。
func (a *AnalyzeByJSoup) GetStringList(ruleStr string) ([]string, error) {
	textS := []string{}
	if ruleStr == "" {
		return textS, nil
	}
	srcRule := parseJSoupSourceRule(ruleStr)

	if strings.TrimSpace(srcRule.elementsRule) == "" {
		textS = append(textS, ElementData(a.element))
		return textS, nil
	}

	ra := NewRuleAnalyzer(srcRule.elementsRule, false)
	ruleStrS, err := ra.SplitRule("&&", "||", "%%")
	if err != nil {
		return nil, err
	}

	var results [][]string
	for _, ruleStrX := range ruleStrS {
		var temp []string
		if srcRule.isCSS {
			lastIndex := strings.LastIndex(ruleStrX, "@")
			if lastIndex < 0 {
				return nil, fmt.Errorf("CSS 规则必须以 @xxx 结尾: %q", ruleStrX)
			}
			selector := ruleStrX[:lastIndex]
			attr := ruleStrX[lastIndex+1:]
			temp = a.getResultLast(jsoupSelect(a.element, selector), attr)
		} else {
			temp = a.getResultList(ruleStrX)
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
						textS = append(textS, temp[i])
					}
				}
			}
		} else {
			for _, temp := range results {
				textS = append(textS, temp...)
			}
		}
	}
	return textS, nil
}

// GetElements 获取元素列表（含组合符）。
func (a *AnalyzeByJSoup) GetElements(rule string) ([]*html.Node, error) {
	if rule == "" {
		return nil, nil
	}
	var elements []*html.Node
	srcRule := parseJSoupSourceRule(rule)
	ra := NewRuleAnalyzer(srcRule.elementsRule, false)
	ruleStrS, err := ra.SplitRule("&&", "||", "%%")
	if err != nil {
		return nil, err
	}

	var elementsList [][]*html.Node
	if srcRule.isCSS {
		for _, ruleStr := range ruleStrS {
			tempS := jsoupSelect(a.element, ruleStr)
			elementsList = append(elementsList, tempS)
			if len(tempS) > 0 && ra.elementsType == "||" {
				break
			}
		}
	} else {
		for _, ruleStr := range ruleStrS {
			rsRule := NewRuleAnalyzer(ruleStr, false)
			rsRule.Trim()
			rs, err := rsRule.SplitRule("@")
			if err != nil {
				return nil, err
			}
			var el []*html.Node
			if len(rs) > 1 {
				el = []*html.Node{a.element}
				for _, rl := range rs {
					var es []*html.Node
					for _, et := range el {
						sub, err := a.getElementsSingle(et, rl)
						if err != nil {
							return nil, err
						}
						es = append(es, sub...)
					}
					el = es
				}
			} else {
				el, err = a.getElementsSingle(a.element, ruleStr)
				if err != nil {
					return nil, err
				}
			}
			elementsList = append(elementsList, el)
			if len(el) > 0 && ra.elementsType == "||" {
				break
			}
		}
	}
	if len(elementsList) > 0 {
		if ra.elementsType == "%%" {
			for i := 0; i < len(elementsList[0]); i++ {
				for _, es := range elementsList {
					if i < len(es) {
						elements = append(elements, es[i])
					}
				}
			}
		} else {
			for _, es := range elementsList {
				elements = append(elements, es...)
			}
		}
	}
	return elements, nil
}

// getResultList 获取内容列表（默认 jsoup 语法主体）。
func (a *AnalyzeByJSoup) getResultList(ruleStr string) []string {
	if ruleStr == "" {
		return nil
	}
	elements := []*html.Node{a.element}

	rule := NewRuleAnalyzer(ruleStr, false)
	rule.Trim()
	rules, err := rule.SplitRule("@")
	if err != nil {
		return nil
	}
	if len(rules) == 0 {
		return nil
	}
	last := len(rules) - 1
	for i := 0; i < last; i++ {
		var es []*html.Node
		for _, elt := range elements {
			sub, err := a.getElementsSingle(elt, rules[i])
			if err != nil {
				return nil
			}
			es = append(es, sub...)
		}
		elements = es
	}
	if len(elements) == 0 {
		return nil
	}
	return a.getResultLast(elements, rules[last])
}

// getResultLast 根据最后一个规则获取内容。
func (a *AnalyzeByJSoup) getResultLast(elements []*html.Node, lastRule string) []string {
	textS := []string{}
	switch lastRule {
	case "text":
		for _, element := range elements {
			text := ElementText(element)
			if text != "" {
				textS = append(textS, text)
			}
		}
	case "textNodes":
		for _, element := range elements {
			var tn []string
			for _, item := range ElementTextNodes(element) {
				text := strings.TrimSpace(item.Data)
				if text != "" {
					tn = append(tn, text)
				}
			}
			if len(tn) > 0 {
				textS = append(textS, strings.Join(tn, "\n"))
			}
		}
	case "ownText":
		for _, element := range elements {
			text := ElementOwnText(element)
			if text != "" {
				textS = append(textS, text)
			}
		}
	case "html":
		// jsoup: elements.select("script").remove() 后再 outerHtml
		var parts []string
		for _, e := range elements {
			parts = append(parts, OuterHTMLClean(e))
		}
		if h := strings.Join(parts, ""); strings.TrimSpace(h) != "" {
			textS = append(textS, h)
		}
	case "all":
		var parts []string
		for _, e := range elements {
			parts = append(parts, OuterHTML(e))
		}
		textS = append(textS, strings.Join(parts, ""))
	default:
		for _, element := range elements {
			url := Attr(element, lastRule)
			if url == "" || containsStr(textS, url) {
				continue
			}
			textS = append(textS, url)
		}
	}
	return textS
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---------------- ElementsSingle：前置选择器 + 索引 ----------------

// indexSpec 索引项：区间为 Triple 或单索引。
type indexSpec struct {
	Interval *[3]int // start, end, step（start/end 可为 math.MinInt 表示省略）
	Single   *int
}

type elementsSingle struct {
	split        byte // '.' 选取 / '!' 排除 / ' ' 不过滤
	beforeRule   string
	indexDefault []int
	indexes      []indexSpec
}

// getElementsSingle 按一条规则取元素（含索引筛选）。
func (a *AnalyzeByJSoup) getElementsSingle(temp *html.Node, rule string) ([]*html.Node, error) {
	if temp == nil {
		return nil, nil
	}
	es := &elementsSingle{split: '.'}
	es.findIndexSet(rule)

	var elements []*html.Node
	if es.beforeRule == "" {
		elements = childElements(temp)
	} else {
		rules := strings.Split(es.beforeRule, ".")
		switch rules[0] {
		case "children":
			elements = childElements(temp)
		case "class":
			if len(rules) < 2 {
				return nil, fmt.Errorf("class 规则缺少参数: %q", rule)
			}
			elements = jsoupElementsByClass(temp, rules[1])
		case "tag":
			if len(rules) < 2 {
				return nil, fmt.Errorf("tag 规则缺少参数: %q", rule)
			}
			elements = jsoupElementsByTag(temp, rules[1])
		case "id":
			if len(rules) < 2 {
				return nil, fmt.Errorf("id 规则缺少参数: %q", rule)
			}
			elements = jsoupElementsById(temp, rules[1])
		case "text":
			if len(rules) < 2 {
				return nil, fmt.Errorf("text 规则缺少参数: %q", rule)
			}
			elements = jsoupElementsContainingOwnText(temp, rules[1])
		default:
			elements = jsoupSelect(temp, es.beforeRule)
		}
	}

	length := len(elements)
	if length == 0 {
		return elements, nil
	}
	// 有序去重索引集合：逆向遍历插入以还原顺序
	indexSet := newOrderedIntSet()

	if len(es.indexes) == 0 {
		for ix := len(es.indexDefault) - 1; ix >= 0; ix-- {
			it := es.indexDefault[ix]
			if it >= 0 && it < length {
				indexSet.add(it)
			} else if it < 0 && length >= -it {
				indexSet.add(it + length)
			}
		}
	} else {
		for ix := len(es.indexes) - 1; ix >= 0; ix-- {
			spec := es.indexes[ix]
			if spec.Interval != nil {
				startX, endX, stepX := spec.Interval[0], spec.Interval[1], spec.Interval[2]
				start := startX
				if start == indexOmitted {
					start = 0
				}
				if start < 0 {
					start += length
				}
				end := endX
				if end == indexOmitted {
					end = length - 1
				}
				if end < 0 {
					end += length
				}
				if (start < 0 && end < 0) || (start >= length && end >= length) {
					continue
				}
				if start >= length {
					start = length - 1
				} else if start < 0 {
					start = 0
				}
				if end >= length {
					end = length - 1
				} else if end < 0 {
					end = 0
				}
				if start == end || stepX >= length {
					indexSet.add(start)
					continue
				}
				step := stepX
				if step > 0 {
					// ok
				} else if -step < length {
					step += length
				} else {
					step = 1
				}
				if end > start {
					for i := start; i <= end; i += step {
						indexSet.add(i)
					}
				} else {
					for i := start; i >= end; i -= step {
						indexSet.add(i)
					}
				}
			} else {
				it := *spec.Single
				if it >= 0 && it < length {
					indexSet.add(it)
				} else if it < 0 && length >= -it {
					indexSet.add(it + length)
				}
			}
		}
	}

	switch es.split {
	case '!':
		for _, pcInt := range indexSet.desc() {
			elements = Deselect(elements, pcInt)
		}
	case '.':
		var sel []*html.Node
		for _, pcInt := range indexSet.values() {
			if pcInt >= 0 && pcInt < len(elements) {
				sel = append(sel, elements[pcInt])
			}
		}
		elements = sel
	}
	return elements, nil
}

const indexOmitted = -1 << 30

// childElements 返回直接子元素。
func childElements(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			out = append(out, c)
		}
	}
	return out
}

// findIndexSet 解析尾部索引（legado findIndexSet 移植，逆向扫描）。
func (es *elementsSingle) findIndexSet(rule string) {
	rus := strings.TrimFunc(rule, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if rus == "" {
		es.split = ' '
		es.beforeRule = rus
		return
	}
	length := len(rus)
	var curInt *int
	curMinus := false
	var curList []*int // 区间暂存（右端、间隔）
	l := ""            // 暂存数字字符串

	head := rus[length-1] == ']'
	parseInt := func(s string) *int {
		v, err := strconv.Atoi(s)
		if err != nil {
			return nil
		}
		return &v
	}

	if head {
		length-- // 跳过尾部 ']'（Kotlin len-- 语义）
		for {
			// Kotlin while (len-- >= 0)：先判断再自减，循环体使用自减后的下标
			if length < 0 {
				break
			}
			length--
			if length < 0 {
				break // Kotlin 此处会越界；这里安全退出
			}
			rl := rus[length]
			if rl == ' ' {
				continue
			}
			if rl >= '0' && rl <= '9' {
				l = string(rl) + l
				continue
			}
			if rl == '-' {
				curMinus = true
				continue
			}
			curInt = nil
			if l != "" {
				curInt = parseInt(l)
				if curMinus && curInt != nil {
					v := -*curInt
					curInt = &v
				}
			}
			switch rl {
			case ':':
				curList = append(curList, curInt)
			default:
				if len(curList) == 0 {
					if curInt == nil {
						// 非索引结构，跳出
						es.split = ' '
						es.beforeRule = rus
						return
					}
					es.indexes = append(es.indexes, indexSpec{Single: curInt})
				} else {
					start := curInt
					end := curList[len(curList)-1]
					step := 1
					if len(curList) == 2 {
						step = deref(curList[0], 1)
					}
					es.indexes = append(es.indexes, indexSpec{Interval: &[3]int{derefOmit(start), derefOmit(end), step}})
					curList = nil
				}
				if rl == '!' {
					es.split = '!'
					length--
					for length > 0 && rus[length] == ' ' {
						length--
					}
					rl = rus[length]
				}
				if rl == '[' {
					es.beforeRule = rus[:length]
					return
				}
				if rl != ',' {
					break
				}
			}
			l = ""
			curMinus = false
		}
	} else {
		for {
			// Kotlin while (len-- >= 0)：从 N-1 逆向遍历
			if length < 0 {
				break
			}
			length--
			if length < 0 {
				break // 安全退出（Kotlin 会越界，此处优于原实现）
			}
			rl := rus[length]
			if rl == ' ' {
				continue
			}
			if rl >= '0' && rl <= '9' {
				l = string(rl) + l
				continue
			}
			if rl == '-' {
				curMinus = true
				continue
			}
			if rl == '!' || rl == '.' || rl == ':' {
				v := parseInt(l)
				if v == nil {
					break
				}
				if curMinus {
					nv := -*v
					v = &nv
				}
				es.indexDefault = append(es.indexDefault, *v)
				if rl != ':' {
					es.split = rl
					es.beforeRule = rus[:length]
					return
				}
			} else {
				break
			}
			l = ""
			curMinus = false
		}
	}
	es.split = ' '
	es.beforeRule = rus
}

func deref(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// derefOmit 省略的端点返回哨兵值。
func derefOmit(p *int) int {
	if p == nil {
		return indexOmitted
	}
	return *p
}

// orderedIntSet 有序不重复整数集合。
type orderedIntSet struct {
	seen map[int]bool
	list []int
}

func newOrderedIntSet() *orderedIntSet {
	return &orderedIntSet{seen: map[int]bool{}}
}

func (s *orderedIntSet) add(v int) {
	if !s.seen[v] {
		s.seen[v] = true
		s.list = append(s.list, v)
	}
}

func (s *orderedIntSet) values() []int { return s.list }

func (s *orderedIntSet) desc() []int {
	out := make([]int, len(s.list))
	copy(out, s.list)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

var _ = regexp.MustCompile // 保留引用
