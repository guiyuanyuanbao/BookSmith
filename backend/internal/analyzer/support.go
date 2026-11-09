package analyzer

import (
	"container/list"
	"encoding/json"
	"sync"
)

// jsonStrictUnmarshal 严格 JSON 解析。
func jsonStrictUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// jsonLooseUnmarshal 宽松 JSON 解析（容忍单引号等，GSON lenient 语义近似）。
func jsonLooseUnmarshal(b []byte, v any) error {
	// 先尝试标准
	if err := json.Unmarshal(b, v); err == nil {
		return nil
	}
	// 单引号 → 双引号 的保守转换后重试
	s := string(b)
	if len(s) >= 2 {
		s = singleToDoubleQuotes(s)
	}
	return json.Unmarshal([]byte(s), v)
}

// singleToDoubleQuotes 把 JSON 中的单引号字符串替换为双引号（保守实现：仅键值整体包裹场景）。
func singleToDoubleQuotes(s string) string {
	out := make([]byte, 0, len(s)+8)
	inSingle := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			inSingle = !inSingle
			out = append(out, '"')
			continue
		}
		if inSingle && c == '"' {
			out = append(out, '\\', '"')
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// lruCache 简单 LRU 缓存（legado regexCache/scriptCache 语义）。
type lruCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	items    map[string]*list.Element
}

type lruItem struct {
	key   string
	value any
}

func newLRUCache(capacity int) *lruCache {
	return &lruCache{capacity: capacity, ll: list.New(), items: map[string]*list.Element{}}
}

func (c *lruCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*lruItem).value, true
	}
	return nil, false
}

func (c *lruCache) put(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.ll.MoveToFront(e)
		e.Value.(*lruItem).value = value
		return
	}
	e := c.ll.PushFront(&lruItem{key: key, value: value})
	c.items[key] = e
	if c.ll.Len() > c.capacity {
		last := c.ll.Back()
		if last != nil {
			c.ll.Remove(last)
			delete(c.items, last.Value.(*lruItem).key)
		}
	}
}
