package analyzer

import (
	"encoding/xml"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// parseXMLDocument 用 encoding/xml 构建保留大小写的 html.Node 树（供 XPath 查询）。
// legado 对 <?xml 内容使用 jsoup xmlParser（保留大小写），此处语义对齐。
func parseXMLDocument(content string) *html.Node {
	dec := xml.NewDecoder(strings.NewReader(content))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	// 容忍常见非严格实体
	dec.Entity = xml.HTMLEntity

	doc := &html.Node{Type: html.DocumentNode}
	cur := doc

	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			// 解析出错：返回已构建的部分树（宽松处理，与 legado 容错一致）
			if doc.FirstChild == nil {
				return nil
			}
			return doc
		}
		switch t := tok.(type) {
		case xml.StartElement:
			node := &html.Node{
				Type: html.ElementNode,
				Data: t.Name.Local,
				Attr: copyXMLAttrs(t.Attr),
			}
			appendChild(cur, node)
			cur = node
		case xml.EndElement:
			if cur != doc && cur.Type == html.ElementNode {
				cur = cur.Parent
				for cur != doc && cur.Type != html.ElementNode {
					cur = cur.Parent
					if cur == nil {
						cur = doc
						break
					}
				}
			}
		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" {
				appendChild(cur, &html.Node{Type: html.TextNode, Data: string(t)})
			}
		case xml.Comment:
			appendChild(cur, &html.Node{Type: html.CommentNode, Data: string(t)})
		case xml.ProcInst:
			// <?xml ...?> 声明等：忽略
		case xml.Directive:
			appendChild(doc, &html.Node{Type: html.DoctypeNode, Data: string(t)})
		}
	}
	if doc.FirstChild == nil {
		return nil
	}
	return doc
}

func copyXMLAttrs(attrs []xml.Attr) []html.Attribute {
	out := make([]html.Attribute, 0, len(attrs))
	for _, a := range attrs {
		key := a.Name.Local
		if a.Name.Space != "" && a.Name.Space != "xmlns" {
			key = a.Name.Local
		} else if a.Name.Space == "xmlns" {
			key = "xmlns"
		}
		out = append(out, html.Attribute{Key: key, Val: a.Value})
	}
	return out
}

func appendChild(parent *html.Node, child *html.Node) {
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
