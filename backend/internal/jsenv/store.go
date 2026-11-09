package jsenv

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"booksmith/internal/httpx"
)

// CookieStore 内存 Cookie 存储（按二级域名组织），可选落盘。
type CookieStore struct {
	mu   sync.Mutex
	m    map[string]map[string]string // domain -> key -> value
	path string
}

// NewCookieStore 创建 Cookie 存储（path 非空时持久化）。
func NewCookieStore(dataDir string) *CookieStore {
	cs := &CookieStore{m: map[string]map[string]string{}}
	if dataDir != "" {
		cs.path = filepath.Join(dataDir, "cookies.json")
		if b, err := os.ReadFile(cs.path); err == nil {
			_ = json.Unmarshal(b, &cs.m)
		}
	}
	return cs
}

func (cs *CookieStore) persist() {
	if cs.path == "" {
		return
	}
	b, err := json.Marshal(cs.m)
	if err == nil {
		_ = os.WriteFile(cs.path, b, 0o644)
	}
}

// SubDomain 取二级域名（legado getSubDomain 语义）。
func SubDomain(host string) string {
	host = strings.TrimPrefix(host, "www.")
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// GetCookie 取某 URL 域的全部 Cookie（k=v; k2=v2 形式）。
func (cs *CookieStore) GetCookie(urlStr string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		return ""
	}
	domain := SubDomain(u.Hostname())
	cs.mu.Lock()
	defer cs.mu.Unlock()
	kv := cs.m[domain]
	if len(kv) == 0 {
		return ""
	}
	// 按键排序保证输出稳定
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+kv[k])
	}
	return strings.Join(parts, "; ")
}

// GetKey 取某 URL 域的指定 Cookie 键。
func (cs *CookieStore) GetKey(urlStr, key string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		return ""
	}
	domain := SubDomain(u.Hostname())
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.m[domain][key]
}

// SetCookie 写入某域的 Cookie 串（k=v; k2=v2）。
func (cs *CookieStore) SetCookie(urlStr, cookie string) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return
	}
	domain := SubDomain(u.Hostname())
	cs.mu.Lock()
	defer cs.mu.Unlock()
	kv := cs.m[domain]
	if kv == nil {
		kv = map[string]string{}
	}
	for _, pair := range strings.Split(cookie, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	cs.m[domain] = kv
	cs.persist()
}

// ReplaceCookie 替换某域 Cookie 的一个键值。
func (cs *CookieStore) ReplaceCookie(urlStr, key, value string) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return
	}
	domain := SubDomain(u.Hostname())
	cs.mu.Lock()
	defer cs.mu.Unlock()
	kv := cs.m[domain]
	if kv == nil {
		kv = map[string]string{}
		cs.m[domain] = kv
	}
	kv[key] = value
	cs.persist()
}

// RemoveCookie 删除某域的指定 Cookie 键。
func (cs *CookieStore) RemoveCookie(urlStr, key string) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return
	}
	domain := SubDomain(u.Hostname())
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if kv := cs.m[domain]; kv != nil {
		delete(kv, key)
	}
	cs.persist()
}

// ---------------- CacheManager ----------------

type cacheEntry struct {
	Value    string `json:"v"`
	ExpireAt int64  `json:"e"` // unix 秒，0 不过期
}

// CacheManager 缓存管理器：内存 + 磁盘（legado CacheManager 子集）。
type CacheManager struct {
	mu      sync.Mutex
	memory  map[string]cacheEntry
	diskDir string
	persist bool
}

// NewCacheManager 创建缓存管理器。
func NewCacheManager(dataDir string) *CacheManager {
	cm := &CacheManager{memory: map[string]cacheEntry{}}
	if dataDir != "" {
		cm.diskDir = filepath.Join(dataDir, "cache")
		_ = os.MkdirAll(cm.diskDir, 0o755)
		cm.persist = true
	}
	return cm
}

func (cm *CacheManager) diskPath(key string) string {
	return filepath.Join(cm.diskDir, sanitizeKey(key)+".json")
}

func sanitizeKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() > 80 {
			break
		}
	}
	return b.String()
}

// Put 写缓存（saveTime 秒后过期；0 永久）。
func (cm *CacheManager) Put(key, value string, saveTime int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	var exp int64
	switch {
	case saveTime > 0:
		exp = time.Now().Add(time.Duration(saveTime) * time.Second).Unix()
	case saveTime < 0:
		exp = time.Now().Unix() - 1 // 立即过期
	}
	cm.memory[key] = cacheEntry{Value: value, ExpireAt: exp}
	if cm.persist && saveTime >= 0 {
		b, _ := json.Marshal(cacheEntry{Value: value, ExpireAt: exp})
		_ = os.WriteFile(cm.diskPath(key), b, 0o644)
	}
}

// Get 读缓存（先内存后磁盘）。
func (cm *CacheManager) Get(key string) string {
	cm.mu.Lock()
	e, ok := cm.memory[key]
	cm.mu.Unlock()
	if ok {
		if e.ExpireAt > 0 && time.Now().Unix() > e.ExpireAt {
			cm.Delete(key)
			return ""
		}
		return e.Value
	}
	if cm.persist {
		b, err := os.ReadFile(cm.diskPath(key))
		if err == nil {
			var de cacheEntry
			if json.Unmarshal(b, &de) == nil {
				if de.ExpireAt > 0 && time.Now().Unix() > de.ExpireAt {
					cm.Delete(key)
					return ""
				}
				cm.mu.Lock()
				cm.memory[key] = de
				cm.mu.Unlock()
				return de.Value
			}
		}
	}
	return ""
}

// GetInt 取整型缓存。
func (cm *CacheManager) GetInt(key string) int64 {
	v := cm.Get(key)
	if v == "" {
		return 0
	}
	var n int64
	json.Unmarshal([]byte(v), &n)
	return n
}

// Delete 删缓存。
func (cm *CacheManager) Delete(key string) {
	cm.mu.Lock()
	delete(cm.memory, key)
	cm.mu.Unlock()
	if cm.persist {
		_ = os.Remove(cm.diskPath(key))
	}
}

// PutMemory 仅内存缓存。
func (cm *CacheManager) PutMemory(key, value string) {
	cm.mu.Lock()
	cm.memory[key] = cacheEntry{Value: value}
	cm.mu.Unlock()
}

// GetFromMemory 取内存缓存。
func (cm *CacheManager) GetFromMemory(key string) string {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	e, ok := cm.memory[key]
	if !ok {
		return ""
	}
	if e.ExpireAt > 0 && time.Now().Unix() > e.ExpireAt {
		return ""
	}
	return e.Value
}

// DeleteMemory 删内存缓存。
func (cm *CacheManager) DeleteMemory(key string) {
	cm.mu.Lock()
	delete(cm.memory, key)
	cm.mu.Unlock()
}

// PutFile 写文件缓存（内容为文本）。
func (cm *CacheManager) PutFile(key, value string) {
	if !cm.persist {
		return
	}
	_ = os.WriteFile(filepath.Join(cm.diskDir, sanitizeKey(key)+".txt"), []byte(value), 0o644)
}

// GetFile 读文件缓存。
func (cm *CacheManager) GetFile(key string) string {
	if !cm.persist {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(cm.diskDir, sanitizeKey(key)+".txt"))
	if err != nil {
		return ""
	}
	return string(b)
}

// ApplySourceRateLimit 将书源 concurrentRate 应用到客户端限流。
// legado 语义：固定窗口 N 次/M 毫秒；纯整数 M 等价 1 次/M 毫秒。
func (e *Env) ApplySourceRateLimit(sourceURL, concurrentRate string) {
	s := strings.TrimSpace(concurrentRate)
	if s == "" || s == "0" {
		return
	}
	if strings.Contains(s, "/") {
		count, window := httpx.ParseConcurrentRate(s)
		if count > 0 && window > 0 {
			// 折算为每次请求最小间隔（保守实现，避免突发突发流量）
			e.Client.SetRateLimit(sourceURL, 1, window/time.Duration(count))
		}
		return
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		e.Client.SetRateLimit(sourceURL, 1, time.Duration(n)*time.Millisecond)
	}
}
