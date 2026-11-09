package analyzer

import (
	"bytes"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// dom.go 提供 jsoup 语义的 DOM 工具（go 的 html.Node 之上）。
// 与 goquery.Find（仅后代）不同，jsoup 的 select/getElementsByX 包含根元素自身（文档序）。

// jsoupSelect 等价 jsoup Element.select(query)：从 root（含自身）按文档序选出匹配元素。
func jsoupSelect(root *html.Node, css string) []*html.Node {
	sel, err := cascadia.Compile(css)
	if err != nil {
		return nil
	}
	return sel.MatchAll(root)
}

// jsoupElementsByClass 等价 jsoup getElementsByClass。
func jsoupElementsByClass(root *html.Node, class string) []*html.Node {
	return jsoupSelect(root, "."+cssEscapeIdent(class))
}

// jsoupElementsByTag 等价 jsoup getElementsByTag。
func jsoupElementsByTag(root *html.Node, tag string) []*html.Node {
	return jsoupSelect(root, tag)
}

// jsoupElementsById 等价 jsoup Collector.collect(Evaluator.Id(id), root)。
func jsoupElementsById(root *html.Node, id string) []*html.Node {
	return jsoupSelect(root, "#"+cssEscapeIdent(id))
}

// cssEscapeIdent 对标识符做最小转义（处理含特殊字符的 class/id）。
func cssEscapeIdent(s string) string {
	if s == "" {
		return s
	}
	needEscape := false
	for _, r := range s {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r > 127) {
			needEscape = true
			break
		}
	}
	if !needEscape {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r > 127 {
			b.WriteRune(r)
		} else {
			b.WriteByte('\\')
			b.WriteRune(r)
		}
	}
	return b.String()
}

// traverseIncludeSelf 按文档序遍历 root 及其后代。
func traverseIncludeSelf(root *html.Node, fn func(n *html.Node) bool) {
	if !fn(root) {
		return
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		traverseIncludeSelf(c, fn)
	}
}

// jsoupElementsContainingOwnText 等价 jsoup getElementsContainingOwnText（大小写敏感包含）。
func jsoupElementsContainingOwnText(root *html.Node, text string) []*html.Node {
	var out []*html.Node
	if text == "" {
		return out
	}
	traverseIncludeSelf(root, func(n *html.Node) bool {
		if n.Type == html.ElementNode && strings.Contains(ElementOwnText(n), text) {
			out = append(out, n)
		}
		return true
	})
	return out
}

// blockTags 块级元素（jsoup isBlock 子集）：进入时补空格。
var blockTags = map[string]bool{
	"p": true, "div": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "li": true, "tr": true, "section": true,
	"article": true, "aside": true, "blockquote": true, "dd": true, "dl": true,
	"dt": true, "hr": true, "ul": true, "ol": true, "form": true,
	"address": true, "pre": true, "table": true, "fieldset": true,
	"figcaption": true, "figure": true, "footer": true, "header": true,
	"main": true, "nav": true, "option": true, "summary": true,
}

// ElementText 等价 jsoup Element.text()：后代全部文本，空白折叠；
// 块级元素边界插入空格（jsoup 语义：a<p>b</p>c → "a b c"）。
func ElementText(n *html.Node) string {
	var b strings.Builder
	appendElementText(n, &b)
	return strings.Join(strings.Fields(b.String()), " ")
}

func appendElementText(n *html.Node, b *strings.Builder) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
		return
	}
	if n.Type != html.ElementNode && n.Type != html.DocumentNode {
		return
	}
	isBlock := false
	if n.Type == html.ElementNode {
		isBlock = blockTags[strings.ToLower(n.Data)]
	}
	if isBlock && b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
		b.WriteByte(' ')
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		appendElementText(c, b)
		if c.Type == html.ElementNode && strings.EqualFold(c.Data, "br") {
			b.WriteByte(' ')
		}
	}
	// 块元素结束（jsoup tail）：后面还有兄弟节点时补空格
	if isBlock && (n.NextSibling != nil || (n.Parent != nil && n.Parent.Type == html.ElementNode)) {
		b.WriteByte(' ')
	}
}

// ElementOwnText 等价 jsoup Element.ownText()：仅直接文本子节点，节点间以空格连接，空白折叠。
func ElementOwnText(n *html.Node) string {
	var parts []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			parts = append(parts, c.Data)
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// ElementTextNodes 返回直接子文本节点（jsoup Element.textNodes()）。
func ElementTextNodes(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			out = append(out, c)
		}
	}
	return out
}

// ElementData 返回 script/style 等元素的内嵌文本（jsoup Element.data()）。
func ElementData(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

// OuterHTML 等价 jsoup outerHtml()。
func OuterHTML(n *html.Node) string {
	if n == nil {
		return ""
	}
	var buf bytes.Buffer
	switch n.Type {
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.DoctypeNode {
				continue
			}
			_ = html.Render(&buf, c)
		}
	case html.TextNode:
		buf.WriteString(n.Data)
	default:
		_ = html.Render(&buf, n)
	}
	return buf.String()
}

// OuterHTMLClean 渲染 HTML 并移除子孙中的 script/style 子树（jsoup elements.select("script").remove() 语义）。
func OuterHTMLClean(n *html.Node) string {
	if n == nil {
		return ""
	}
	var buf bytes.Buffer
	renderClean(&buf, n)
	return buf.String()
}

func renderClean(buf *bytes.Buffer, n *html.Node) {
	clean := cloneClean(n)
	if clean != nil {
		_ = html.Render(buf, clean)
	}
}

// cloneClean 深拷贝节点树并剔除 script/style 子树与注释。
func cloneClean(n *html.Node) *html.Node {
	if n == nil {
		return nil
	}
	switch n.Type {
	case html.ElementNode:
		name := strings.ToLower(n.Data)
		if name == "script" || name == "style" {
			return nil
		}
	case html.CommentNode:
		return nil
	case html.DoctypeNode:
		return nil
	}
	cp := &html.Node{
		Type: n.Type,
		Data: n.Data,
		Attr: append([]html.Attribute(nil), n.Attr...),
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if cc := cloneClean(c); cc != nil {
			appendDomChild(cp, cc)
		}
	}
	return cp
}

func appendDomChild(parent, child *html.Node) {
	child.Parent = parent
	if parent.LastChild != nil {
		parent.LastChild.NextSibling = child
		child.PrevSibling = parent.LastChild
		parent.LastChild = child
	} else {
		parent.FirstChild = child
		parent.LastChild = child
	}
}

// Attr 获取元素属性值。
func Attr(n *html.Node, key string) string {
	if n == nil || n.Type != html.ElementNode {
		return ""
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// Deselect 从切片中移除指定索引的元素（保持其余顺序），用于索引排除语义。
func Deselect(nodes []*html.Node, idx int) []*html.Node {
	if idx < 0 || idx >= len(nodes) {
		return nodes
	}
	out := make([]*html.Node, 0, len(nodes)-1)
	out = append(out, nodes[:idx]...)
	out = append(out, nodes[idx+1:]...)
	return out
}

// parseHTML 用 goquery 兼容的 x/net/html 解析文档。
func parseHTML(content string) (*html.Node, error) {
	return html.Parse(strings.NewReader(content))
}

// goqueryDoc 便捷：基于节点构建 goquery 文档（供部分工具复用）。
func goqueryDoc(n *html.Node) *goquery.Document {
	return goquery.NewDocumentFromNode(n)
}
