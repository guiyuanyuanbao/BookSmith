package jsenv

import (
	"reflect"
	"strings"

	"github.com/dop251/goja"

	"booksmith/internal/model"
)

// bindings.go：JS 绑定层。
// 通过 FieldNameMapper 将 Go 导出方法/字段名首字母小写暴露给 JS，
// 使 java.ajax / book.bookSourceUrl 等 legado 惯用调用无需 Proxy 即可工作。
// 不使用 Proxy 可避免 goja 的 Proxy 实现与复杂脚本解析之间的兼容问题。

// lowerFirstFieldMapper 字段名与方法名首字母小写。
type lowerFirstFieldMapper struct{}

func (lowerFirstFieldMapper) FieldName(t reflect.Type, f reflect.StructField) string {
	if len(f.Name) > 0 {
		return strings.ToLower(f.Name[:1]) + f.Name[1:]
	}
	return f.Name
}

// MethodName 方法名首字母小写（Ajax → ajax, Md5Encode16 → md5Encode16）。
func (lowerFirstFieldMapper) MethodName(t reflect.Type, m reflect.Method) string {
	if len(m.Name) > 0 {
		return strings.ToLower(m.Name[:1]) + m.Name[1:]
	}
	return m.Name
}

// InjectBindings 注入 java/cookie/cache/source/book/chapter 等对象。
// 直接绑定（无 Proxy），由 FieldNameMapper 处理命名映射。
func (en *Engine) InjectBindings(vm *goja.Runtime, javaObj, sourceObj, bookObj, chapterObj any) {
	vm.SetFieldNameMapper(lowerFirstFieldMapper{})
	if javaObj != nil {
		_ = vm.Set("java", javaObj)
	}
	if sourceObj != nil {
		_ = vm.Set("source", sourceObj)
	}
	if bookObj != nil {
		_ = vm.Set("book", bookObj)
	}
	if chapterObj != nil {
		_ = vm.Set("chapter", chapterObj)
	}
	_ = vm.Set("cookie", en.env.Cookies())
	_ = vm.Set("cache", en.env.Cache())
}

// JSBook 书对象（字段名与 legado JS 环境一致）。
type JSBook struct {
	Name          string
	Author        string
	BookUrl       string
	TocUrl        string
	Kind          string
	CoverUrl      string
	Intro         string
	LatestChapter string
	Variable      string
}

// JSChapter 章节对象。
type JSChapter struct {
	Title string
	Url   string
	Index int
}

// JSSource 源对象（字段名与 legado JS 环境一致）。
type JSSource struct {
	BookSourceUrl     string
	BookSourceName    string
	BookSourceGroup   string
	BookSourceType    int
	ConcurrentRate    string
	Header            string
	LoginUrl          string
	LoginUi           string
	BookSourceComment string
	Variable          string

	ext *Extensions
}

// GetKey 源键。
func (s *JSSource) GetKey() string { return s.BookSourceUrl }

// Put 源级变量。
func (s *JSSource) Put(key, value string) string {
	if s.ext != nil && s.ext.vars != nil {
		s.ext.vars.Put("__src_"+s.GetKey()+"_"+key, value)
	}
	return value
}

// Get 源级变量。
func (s *JSSource) Get(key string) string {
	if s.ext != nil && s.ext.vars != nil {
		return s.ext.vars.Get("__src_" + s.GetKey() + "_" + key)
	}
	return ""
}

// PutConcurrent 动态并发率。
func (s *JSSource) PutConcurrent(rate string) {
	if s.ext != nil {
		s.ext.env.ApplySourceRateLimit(s.GetKey(), rate)
	}
}

// GetVariable 源变量。
func (s *JSSource) GetVariable(key string) string { return s.Get(key) }

// SetVariable 源变量。
func (s *JSSource) SetVariable(key, value string) { s.Put(key, value) }

// NewJSSource 从书源实体构建 JS 源对象。
func NewJSSource(bs *model.BookSource, ext *Extensions) *JSSource {
	if bs == nil {
		return &JSSource{ext: ext}
	}
	return &JSSource{
		BookSourceUrl:     bs.BookSourceURL,
		BookSourceName:    bs.BookSourceName,
		BookSourceGroup:   bs.BookSourceGroup,
		BookSourceType:    bs.BookSourceType,
		ConcurrentRate:    bs.ConcurrentRate,
		Header:            bs.Header,
		LoginUrl:          bs.LoginURL,
		LoginUi:           bs.LoginUI,
		BookSourceComment: bs.BookSourceComment,
		Variable:          bs.Variable,
		ext:               ext,
	}
}
