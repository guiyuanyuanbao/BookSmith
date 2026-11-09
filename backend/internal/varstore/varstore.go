// Package varstore 提供规则引擎的变量层级存储（chapter → book → source）。
package varstore

// VariableProvider 变量存取接口。
type VariableProvider interface {
	Put(key, value string)
	Get(key string) string
}

// Variables 简单变量存储（可挂父级形成层级链）。
type Variables struct {
	m      map[string]string
	parent VariableProvider
}

// New 创建变量存储。
func New(parent VariableProvider) *Variables {
	return &Variables{m: map[string]string{}, parent: parent}
}

// NewChain 创建层级链（低优先级 → 高优先级；返回最高优先级端）。
// 例如 NewChain(sourceVars, bookVars) 得到先查 bookVars 再查 sourceVars 的存储。
func NewChain(levels ...VariableProvider) VariableProvider {
	var top VariableProvider
	for i := len(levels) - 1; i >= 0; i-- {
		top = &Variables{m: map[string]string{}, parent: top}
		// 用 levels[i] 包装：查询时先自身再 parent
		lv := levels[i]
		top = &chainLink{self: lv, parent: top}
	}
	return top
}

type chainLink struct {
	self   VariableProvider
	parent VariableProvider
}

func (c *chainLink) Put(key, value string) { c.self.Put(key, value) }

func (c *chainLink) Get(key string) string {
	if v := c.self.Get(key); v != "" {
		return v
	}
	if c.parent != nil {
		return c.parent.Get(key)
	}
	return ""
}

// Put 写变量。
func (v *Variables) Put(key, value string) { v.m[key] = value }

// Get 读变量（未命中回溯父级）。
func (v *Variables) Get(key string) string {
	if val, ok := v.m[key]; ok {
		return val
	}
	if v.parent != nil {
		return v.parent.Get(key)
	}
	return ""
}

// MapProvider 基于 map 的只读变量提供者。
type MapProvider map[string]string

func (m MapProvider) Put(key, value string) { m[key] = value }
func (m MapProvider) Get(key string) string { return m[key] }

// Empty 空实现。
type Empty struct{}

func (Empty) Put(key, value string) {}
func (Empty) Get(key string) string { return "" }
