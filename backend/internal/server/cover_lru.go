// cover_lru.go 封面缓存 LRU：内存热缓存与磁盘索引共用一套驱逐逻辑。
// 内存层淘汰最旧条目（单条驱逐，不再"超 256 整体清空"）；
// 磁盘层按文件数/总字节上限驱逐最冷文件（默认 5000 个或 500MB），
// 解决封面缓存只增不删、长期使用无限增长的问题。
package server

import (
	"container/list"
	"os"
	"path/filepath"
	"sync"
)

// coverCacheLimits 磁盘缓存上限（任一超限即驱逐）。
const (
	coverMaxEntries = 5000
	coverMaxBytes   = 500 << 20 // 500MB
	// memCoverMax 内存热缓存条目上限
	memCoverMax = 256
)

// lruEntry 驱逐队列元素（key 即缓存键：内存层为 rawURL，磁盘层为 hash 文件名）。
// Value 仅内存层使用（数据本体随条目驱逐一并释放）；磁盘层 Value 为 nil。
type lruEntry struct {
	key   string
	size  int64
	value *coverCacheEntry // 内存层非 nil；磁盘层 nil
}

// CoverLRU 并发安全的 LRU 索引。
// 磁盘层构造时给 evict 回调（驱逐即删文件）与字节上限；
// 内存层用 memPut/memGet 存取条目数据（仅条目数上限）。
type CoverLRU struct {
	mu       sync.Mutex
	maxEn    int
	maxBytes int64
	items    map[string]*list.Element // key → queue element
	order    *list.List               // front = 最新，back = 最冷
	total    int64
	// evict 淘汰回调：磁盘层执行 os.Remove，内存层为 nil（仅从索引删除）
	evict func(key string)
}

// NewCoverLRU 创建 LRU；evict 为淘汰回调（可为 nil）。
func NewCoverLRU(maxEntries int, maxBytes int64, evict func(key string)) *CoverLRU {
	if maxEntries <= 0 {
		maxEntries = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 62 // nil 回调（内存层）时实质不按字节驱逐
	}
	return &CoverLRU{
		maxEn:    maxEntries,
		maxBytes: maxBytes,
		items:    map[string]*list.Element{},
		order:    list.New(),
		evict:    evict,
	}
}

// Touch 记录条目写入/访问，提升为最新；新键登记大小，旧键累计差值。
func (l *CoverLRU) Touch(key string, size int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		l.order.MoveToFront(el)
		e := el.Value.(*lruEntry)
		l.total += size - e.size
		e.size = size
	} else {
		l.items[key] = l.order.PushFront(&lruEntry{key: key, size: size})
		l.total += size
	}
	l.trimLocked()
}

// Remove 反注册条目（缓存文件被显式删除时调用）。
func (l *CoverLRU) Remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		l.total -= el.Value.(*lruEntry).size
		l.order.Remove(el)
		delete(l.items, key)
	}
}

// Len 当前条目数。
func (l *CoverLRU) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.items)
}

// trimLocked 驱逐最冷条目直到满足上限（保底保留 1 条）。
func (l *CoverLRU) trimLocked() {
	for len(l.items) > l.maxEn || (len(l.items) > 1 && l.total > l.maxBytes) {
		el := l.order.Back()
		if el == nil {
			return
		}
		e := el.Value.(*lruEntry)
		l.order.Remove(el)
		delete(l.items, e.key)
		l.total -= e.size
		if l.evict != nil {
			l.evict(e.key)
		}
	}
}

// memGet 内存层读（命中自动提温）。
func (l *CoverLRU) memGet(key string) (coverCacheEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.items[key]
	if !ok {
		return coverCacheEntry{}, false
	}
	e := el.Value.(*lruEntry)
	l.order.MoveToFront(el)
	if e.value == nil {
		return coverCacheEntry{}, false
	}
	return *e.value, true
}

// memPut 内存层写（超限淘汰最冷条目，数据随条目释放）。
func (l *CoverLRU) memPut(key string, e coverCacheEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.items[key]; ok {
		l.order.MoveToFront(el)
		el.Value.(*lruEntry).value = &e
		return
	}
	l.items[key] = l.order.PushFront(&lruEntry{key: key, value: &e})
	l.trimMemLocked()
}

func (l *CoverLRU) trimMemLocked() {
	for len(l.items) > l.maxEn {
		el := l.order.Back()
		if el == nil {
			return
		}
		e := el.Value.(*lruEntry)
		l.order.Remove(el)
		delete(l.items, e.key)
		if l.evict != nil {
			l.evict(e.key)
		}
	}
}

// scanCoversDir 启动时扫描磁盘封面目录建立 LRU 索引（文件名 hash 即 key）。
// 非 .img 文件忽略。
func scanCoversDir(coversDir string, lru *CoverLRU) {
	entries, err := os.ReadDir(coversDir)
	if err != nil {
		return
	}
	for _, de := range entries {
		if de.IsDir() || filepath.Ext(de.Name()) != ".img" {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		lru.Touch(de.Name(), info.Size())
	}
}
